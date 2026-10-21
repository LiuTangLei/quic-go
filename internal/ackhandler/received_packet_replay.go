package ackhandler

import "github.com/quic-go/quic-go/internal/protocol"

// replayWindowSize bounds how far behind the largest received packet number a
// late packet may still be processed. It must be a power of two.
const replayWindowSize = 8192

// replayWindow records exactly which recent packet numbers were processed.
// ACK ranges may be pruned (ACK-of-ACK, MaxNumAckRanges) to keep ACK frames
// small; duplicate detection must not depend on that pruning. Otherwise a
// merely reordered packet below the ACK floor is discarded as a "potential
// duplicate", which turns reordering into loss for unretransmitted DATAGRAMs.
// Packets older than the window are still treated as duplicates (RFC 9000,
// Section 12.3).
type replayWindow struct {
	highest protocol.PacketNumber // InvalidPacketNumber until the first packet
	bits    [replayWindowSize / 64]uint64
}

func (w *replayWindow) seen(p protocol.PacketNumber) bool {
	if w.highest == protocol.InvalidPacketNumber || p > w.highest {
		return false
	}
	if w.highest-p >= replayWindowSize {
		return true
	}
	i := uint64(p) % replayWindowSize
	return w.bits[i/64]&(1<<(i%64)) != 0
}

// mark records p and reports whether it was new.
func (w *replayWindow) mark(p protocol.PacketNumber) bool {
	if w.seen(p) {
		return false
	}
	if w.highest == protocol.InvalidPacketNumber || p > w.highest {
		w.advance(p)
	}
	i := uint64(p) % replayWindowSize
	w.bits[i/64] |= 1 << (i % 64)
	return true
}

// advance clears the slots of packet numbers (highest, p] that are reused.
func (w *replayWindow) advance(p protocol.PacketNumber) {
	if w.highest == protocol.InvalidPacketNumber || p-w.highest >= replayWindowSize {
		clear(w.bits[:])
		w.highest = p
		return
	}
	for q := w.highest + 1; q <= p; {
		i := uint64(q) % replayWindowSize
		if i%64 == 0 && p-q >= 63 {
			w.bits[i/64] = 0
			q += 64
			continue
		}
		w.bits[i/64] &^= 1 << (i % 64)
		q++
	}
	w.highest = p
}
