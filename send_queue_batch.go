package quic

import "github.com/quic-go/quic-go/internal/protocol"

// SendBatch transfers ownership of at most eight independent packet buffers.
// Each buffer can have a different length. It occupies one bounded queue slot,
// just as a kernel GSO vector does, without padding or changing UDP boundaries.
func (h *sendQueue) SendBatch(packets []*packetBuffer, ecn protocol.ECN) {
	if len(packets) == 0 {
		return
	}
	if len(packets) > portablePacketBatchSize || h.batchWrite == nil || ecn != protocol.ECNUnsupported {
		panic("invalid portable packet vector")
	}
	entry := queueEntry{vectorLen: uint8(len(packets)), ecn: ecn}
	copy(entry.vector[:], packets)
	select {
	case h.queue <- entry:
		if len(h.queue) == sendQueueCapacity {
			select {
			case <-h.available:
			default:
			}
		}
	case <-h.runStopped:
		releaseQueueEntry(entry)
	default:
		panic("sendQueue.SendBatch would have blocked")
	}
}

func releaseQueueEntry(e queueEntry) {
	if e.vectorLen > 0 {
		for _, b := range e.vector[:e.vectorLen] {
			b.Release()
		}
	} else if e.buf != nil {
		e.buf.Release()
	}
}

// writeReadyBatch preserves datagram boundaries and existing OOB behavior.
// Every dequeued packet is released exactly once even when a writer fails.
func (h *sendQueue) writeReadyBatch(entries []queueEntry) error {
	defer func() {
		for _, e := range entries {
			releaseQueueEntry(e)
		}
	}()
	var data [portablePacketBatchSize][]byte
	n := 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		var err error
		if n == 1 {
			err = h.conn.Write(data[0], 0, protocol.ECNUnsupported)
		} else {
			err = h.batchWrite(data[:n])
		}
		n = 0
		return err
	}
	add := func(b []byte) error {
		data[n] = b
		n++
		if n == len(data) {
			return flush()
		}
		return nil
	}
	for _, e := range entries {
		if e.vectorLen > 0 {
			for _, p := range e.vector[:e.vectorLen] {
				if err := add(p.Data); err != nil {
					return err
				}
			}
			continue
		}
		if e.gsoSize != 0 || e.ecn != protocol.ECNUnsupported {
			if err := flush(); err != nil {
				return err
			}
			if err := h.conn.Write(e.buf.Data, e.gsoSize, e.ecn); err != nil {
				return err
			}
			continue
		}
		if err := add(e.buf.Data); err != nil {
			return err
		}
	}
	return flush()
}
