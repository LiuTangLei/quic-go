package quic

import (
	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/qlog"
)

// appendDatagrams drains only the ready FIFO prefix that fits this packet.
// Keep one lock and one producer notification per packet, rather than a
// Peek/Pop lock pair and notification per frame. Never wait for more data.
func (h *datagramQueue) appendDatagrams(pl payload, maxPayloadSize protocol.ByteCount, limit int, v protocol.Version) payload {
	h.sendMx.Lock()
	removed := false
	var dropped []int
	for range min(limit, maxDatagramSendQueueLen) {
		if h.sendQueue.Empty() {
			break
		}
		f := h.sendQueue.PeekFront()
		size := f.Length(v)
		if size > maxPayloadSize-pl.length {
			if pl.ack == nil && len(pl.frames) == 0 {
				// Preserve the existing unreliable path-MTU drop policy only
				// when the frame cannot fit an otherwise empty packet.
				h.sendQueue.PopFront()
				if h.qlogger != nil {
					dropped = append(dropped, len(f.Data))
				}
				removed = true
				continue
			}
			// Do not skip a FIFO head just because an ACK or earlier frame
			// consumed this packet's remaining space.
			break
		}
		pl.frames = append(pl.frames, ackhandler.Frame{Frame: f})
		pl.length += size
		h.sendQueue.PopFront()
		removed = true
	}
	h.sendMx.Unlock()
	if removed {
		select {
		case h.sent <- struct{}{}:
		default:
		}
	}
	for _, size := range dropped {
		h.qlogger.RecordEvent(qlog.DatagramDropped{Direction: qlog.DirectionSending, Raw: qlog.RawInfo{Length: size}, Trigger: "too_large"})
	}
	return pl
}
