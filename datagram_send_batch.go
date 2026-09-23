package quic

import (
	"errors"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
)

// SendDatagramsWithPrefix queues a bounded batch of independent RFC 9221
// datagrams. Prefix is prepended to EACH payload. Each caller slice is borrowed
// only until return. n is the number accepted before an error; callers must
// never resend those n datagrams. No datagram boundary, on-wire frame, pacing,
// congestion control or retransmission behavior is changed.
//
// This optional API is useful when an IP engine already has a batch ready. It
// never waits to accumulate application data and retains the existing 32-frame
// send queue bound. Validation failures enqueue nothing.
func (c *Conn) SendDatagramsWithPrefix(prefix []byte, payloads [][]byte) (int, error) {
	if len(payloads) == 0 {
		return 0, nil
	}
	if len(payloads) > 128 {
		return 0, errors.New("datagram batch exceeds 128 packets")
	}
	if !c.supportsDatagrams() {
		return 0, errors.New("datagram support disabled")
	}
	f := wire.DatagramFrame{DataLenPresent: true}
	limit := min(f.MaxDataLen(c.peerParams.MaxDatagramFrameSize, c.version), protocol.ByteCount(c.maxPayloadSizeEstimate.Load()))
	for _, p := range payloads {
		if protocol.ByteCount(len(prefix)) > limit || protocol.ByteCount(len(p)) > limit-protocol.ByteCount(len(prefix)) {
			return 0, &DatagramTooLargeError{MaxDatagramPayloadSize: int64(limit)}
		}
	}
	accepted := 0
	for len(payloads) > 0 {
		count := min(len(payloads), maxDatagramSendQueueLen)
		// Own storage per bounded group, not two heap objects per datagram.
		// The packer may retain a frame after Pop, so do not recycle here.
		// Ordinary Go reachability keeps the whole group alive until the
		// last frame is consumed. No packet, prefix or framing is shared
		// with the caller, and each payload's capacity ends at its boundary.
		frameStorage := make([]wire.DatagramFrame, count)
		size := 0
		for _, p := range payloads[:count] {
			size += len(prefix) + len(p)
		}
		storage := make([]byte, size)
		var frames [maxDatagramSendQueueLen]*wire.DatagramFrame
		offset := 0
		for i, p := range payloads[:count] {
			end := offset + len(prefix) + len(p)
			data := storage[offset:end:end]
			copy(data, prefix)
			copy(data[len(prefix):], p)
			frameStorage[i] = wire.DatagramFrame{DataLenPresent: true, Data: data}
			frames[i] = &frameStorage[i]
			offset = end
		}
		n, err := c.datagramQueue.addBatch(frames[:count])
		accepted += n
		if err != nil {
			return accepted, err
		}
		payloads = payloads[count:]
	}
	return accepted, nil
}

// addBatch wakes the connection loop once per available group rather than
// once per packet. Already accepted data is owned by the queue. A full queue
// applies the same cancellable backpressure as Add; it is not expanded.
func (h *datagramQueue) addBatch(frames []*wire.DatagramFrame) (int, error) {
	n := 0
	h.sendMx.Lock()
	for n < len(frames) {
		select {
		case <-h.closed:
			h.sendMx.Unlock()
			return n, h.closeErr
		default:
		}
		available := maxDatagramSendQueueLen - h.sendQueue.Len()
		if available > 0 {
			end := min(len(frames), n+available)
			for _, f := range frames[n:end] {
				h.sendQueue.PushBack(f)
			}
			n = end
			h.sendMx.Unlock()
			h.hasData()
			if n == len(frames) {
				return n, nil
			}
			h.sendMx.Lock()
			continue
		}
		select {
		case <-h.sent:
		default:
		}
		h.sendMx.Unlock()
		select {
		case <-h.closed:
			return n, h.closeErr
		case <-h.sent:
		}
		h.sendMx.Lock()
	}
	h.sendMx.Unlock()
	return n, nil
}
