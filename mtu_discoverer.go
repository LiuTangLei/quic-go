package quic

import (
	"time"

	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

const (
	// At some point, we have to stop searching for a higher MTU.
	// We're happy to send a packet that's 10 bytes smaller than the actual MTU.
	maxMTUDiff protocol.ByteCount = 20
	// send a probe packet every mtuProbeDelay RTTs
	mtuProbeDelay = 5
	// Once maxLostMTUProbes MTU probe packets larger than a certain size are lost,
	// MTU discovery won't probe for larger MTUs than this size.
	// The algorithm used here is resilient to packet loss of (maxLostMTUProbes - 1) packets.
	maxLostMTUProbes = 3
	// In target mode, a size that the path did not carry is tried again at
	// this interval: the path under a connection can change without notice.
	mtuTargetRetryInterval = time.Minute
	// In target mode, a size in use is confirmed again at this interval.
	// Without DF nothing else reveals a path that stopped carrying it.
	mtuTargetValidateInterval = 20 * time.Second
)

// The Path MTU is found by sending a larger packet every now and then.
// If the packet is acknowledged, we conclude that the path supports this larger packet size.
// If the packet is lost, this can mean one of two things:
//   1. The path doesn't support this larger packet size, or
//   2. The packet was lost due to packet loss, independent of its size.
// The algorithm used here is resilient to packet loss of (maxLostMTUProbes - 1) packets.
// For simplicty, the following example use maxLostMTUProbes = 2.
//
// Initialization:
//    |------------------------------------------------------------------------------|
//   min                                                                            max
//
// The first MTU probe packet will have size (min+max)/2.
// Assume that this packet is acknowledged. We can now move the min marker,
// and continue the search in the resulting interval.
//
// If 1st probe packet acknowledged:
//    |---------------------------------------|--------------------------------------|
//                                           min                                    max
//
// If 1st probe packet lost:
//    |---------------------------------------|--------------------------------------|
//   min                                    lost[0]                                 max
//
// We can't conclude that the path doesn't support this packet size, since the loss of the probe
// packet could have been unrelated to the packet size.  A larger probe packet will be sent later on.
// After a loss, the next probe packet has size (min+lost[0])/2.
// Now assume this probe packet is acknowledged:
//
// 2nd probe packet acknowledged:
//    |------------------|--------------------|--------------------------------------|
//                      min                lost[0]                                  max
//
// First of all, we conclude that the path supports at least this MTU. That's progress!
// Second, we probe a bit more aggressively with the next probe packet:
// After an acknowledgement, the next probe packet has size (min+max)/2.
// This means we'll send a packet larger than the first probe packet (which was lost).
//
// If 3rd probe packet acknowledged:
//    |-------------------------------------------------|----------------------------|
//                                                     min                          max
//
// We can conclude that the loss of the 1st probe packet was not due to its size, and
// continue searching in a much smaller interval now.
//
// If 3rd probe packet lost:
//    |------------------|--------------------|---------|----------------------------|
//                      min                lost[0]     max
//
// Since in our example numPTOProbes = 2, and we lost 2 packets smaller than max, we
// conclude that this packet size is not supported on the path, and reduce the maximum
// value of the search interval.
//
// MTU discovery concludes once the interval min and max has been narrowed down to maxMTUDiff.

type mtuFinder struct {
	lastProbeTime monotime.Time

	rttStats *utils.RTTStats

	inFlight protocol.ByteCount // the size of the probe packet currently in flight. InvalidByteCount if none is in flight
	min      protocol.ByteCount

	// on initialization, we treat the maximum size as the first "lost" packet
	lost             [maxLostMTUProbes]protocol.ByteCount
	lastProbeWasLost bool

	// The generation is used to ignore ACKs / losses for probe packets sent before a reset.
	// Resets happen when the connection is migrated to a new path.
	// We're therefore not concerned about overflows of this counter.
	generation uint8

	// Target mode (Config.PacketSizeTarget): instead of searching, probe one
	// size, use it once acknowledged, keep confirming it, and return to base
	// when maxLostMTUProbes consecutive probes of it are lost. target is zero
	// when searching.
	base, target protocol.ByteCount
	targetLost   int  // consecutive lost probes of target
	reverted     bool // min returned to base since the last takeReverted

	confirmUrgent, baseFeedbackSent bool
	qlogger                         qlogwriter.Recorder
}

// probeOnly switches to target mode, starting from the current size.
func (f *mtuFinder) probeOnly(target protocol.ByteCount) {
	f.base, f.target, f.targetLost = f.min, target, 0
}

// targetDelay is the time between two probes in target mode.
func (f *mtuFinder) targetDelay() time.Duration {
	quick := mtuProbeDelay * f.rttStats.SmoothedRTT()
	switch {
	case f.min < f.target && f.targetLost >= maxLostMTUProbes:
		return max(quick, mtuTargetRetryInterval)
	case f.confirmUrgent:
		return quick
	case f.min >= f.target && f.targetLost == 0:
		return max(quick, mtuTargetValidateInterval)
	default:
		// Trying a new size, or re-checking one whose last probe was lost.
		return quick
	}
}

// takeReverted reports, once, that the size in use was withdrawn.
func (f *mtuFinder) takeReverted() bool {
	reverted := f.reverted
	f.reverted = false
	return reverted
}

func newMTUDiscoverer(
	rttStats *utils.RTTStats,
	start, max protocol.ByteCount,
	qlogger qlogwriter.Recorder,
) *mtuFinder {
	f := &mtuFinder{
		inFlight: protocol.InvalidByteCount,
		rttStats: rttStats,
		qlogger:  qlogger,
	}
	f.init(start, max)
	return f
}

func (f *mtuFinder) init(start, max protocol.ByteCount) {
	f.min = start
	for i := range f.lost {
		if i == 0 {
			f.lost[i] = max
			continue
		}
		f.lost[i] = protocol.InvalidByteCount
	}
}

func (f *mtuFinder) done() bool {
	if f.target != 0 {
		return f.min >= f.target
	}
	return f.max()-f.min <= maxMTUDiff+1
}

func (f *mtuFinder) max() protocol.ByteCount {
	for i, v := range f.lost {
		if v == protocol.InvalidByteCount {
			return f.lost[i-1]
		}
	}
	return f.lost[len(f.lost)-1]
}

func (f *mtuFinder) Start(now monotime.Time) {
	f.lastProbeTime = now // makes sure the first probe packet is not sent immediately
}

func (f *mtuFinder) ShouldSendProbe(now monotime.Time) bool {
	if f.lastProbeTime.IsZero() {
		return false
	}
	if f.target != 0 {
		return f.inFlight == protocol.InvalidByteCount && !now.Before(f.lastProbeTime.Add(f.targetDelay()))
	}
	if f.inFlight != protocol.InvalidByteCount || f.done() {
		return false
	}
	return !now.Before(f.lastProbeTime.Add(mtuProbeDelay * f.rttStats.SmoothedRTT()))
}

func (f *mtuFinder) GetPing(now monotime.Time) (ackhandler.Frame, protocol.ByteCount) {
	var size protocol.ByteCount
	if f.target != 0 {
		size = f.target
	} else if f.lastProbeWasLost {
		size = (f.min + f.lost[0]) / 2
	} else {
		size = (f.min + f.max()) / 2
	}
	f.lastProbeTime = now
	f.inFlight = size
	f.baseFeedbackSent = false
	return ackhandler.Frame{
		Frame:   &wire.PingFrame{},
		Handler: &mtuFinderAckHandler{mtuFinder: f, generation: f.generation},
	}, size
}

func (f *mtuFinder) CurrentSize() protocol.ByteCount {
	return f.min
}

func (f *mtuFinder) Reset(start, max protocol.ByteCount) {
	f.generation++
	f.lastProbeTime = 0
	f.lastProbeWasLost = false
	f.inFlight = protocol.InvalidByteCount
	f.base, f.target, f.targetLost, f.reverted = 0, 0, 0, false
	f.confirmUrgent, f.baseFeedbackSent = false, false
	f.init(start, max)
}

type mtuFinderAckHandler struct {
	*mtuFinder
	generation uint8
}

var _ ackhandler.FrameHandler = &mtuFinderAckHandler{}

func (h *mtuFinderAckHandler) OnAcked(wire.Frame) {
	if h.generation != h.mtuFinder.generation {
		// ACK for probe sent before reset
		return
	}
	size := h.inFlight
	if size == protocol.InvalidByteCount {
		panic("OnAcked callback called although there's no MTU probe packet in flight")
	}
	h.inFlight = protocol.InvalidByteCount
	if h.target != 0 {
		grew := h.min < size
		h.min, h.targetLost = size, 0
		h.confirmUrgent = false
		if grew && h.qlogger != nil {
			h.qlogger.RecordEvent(qlog.MTUUpdated{Value: int(size), Done: true})
		}
		return
	}
	h.min = size
	h.lastProbeWasLost = false
	// remove all values smaller than size from the lost array
	var j int
	for i, v := range h.lost {
		if size < v {
			j = i
			break
		}
	}
	if j > 0 {
		for i := range len(h.lost) {
			if i+j < len(h.lost) {
				h.lost[i] = h.lost[i+j]
			} else {
				h.lost[i] = protocol.InvalidByteCount
			}
		}
	}
	if h.qlogger != nil {
		h.qlogger.RecordEvent(qlog.MTUUpdated{
			Value: int(size),
			Done:  h.done(),
		})
	}
}

func (h *mtuFinderAckHandler) OnLost(wire.Frame) {
	if h.generation != h.mtuFinder.generation {
		// probe sent before reset received
		return
	}
	size := h.inFlight
	if size == protocol.InvalidByteCount {
		panic("OnLost callback called although there's no MTU probe packet in flight")
	}
	h.inFlight = protocol.InvalidByteCount
	if h.target != 0 {
		h.targetLost++
		if h.min >= h.target && h.targetLost >= maxLostMTUProbes {
			// The path no longer carries the size in use.
			h.min, h.reverted = h.base, true
			h.confirmUrgent = false
			if h.qlogger != nil {
				h.qlogger.RecordEvent(qlog.MTUUpdated{Value: int(h.base)})
			}
		}
		return
	}
	h.lastProbeWasLost = true
	for i, v := range h.lost {
		if size < v {
			copy(h.lost[i+1:], h.lost[i:])
			h.lost[i] = size
			break
		}
	}
}

// A successful target probe can become obsolete when the local route MTU
// shrinks. Invalidate its callbacks before withdrawing: a late ACK for the
// former path must not raise the size again or touch a different probe.
func (f *mtuFinder) withdrawAfterWriteError(size protocol.ByteCount, now monotime.Time) bool {
	if f.target == 0 || f.min <= f.base || size <= f.base {
		return false
	}
	base, target, maximum := f.base, f.target, f.max()
	f.Reset(base, maximum)
	f.probeOnly(target)
	f.targetLost = maxLostMTUProbes
	f.lastProbeTime = now
	f.reverted = true
	if f.qlogger != nil {
		f.qlogger.RecordEvent(qlog.MTUUpdated{Value: int(base)})
	}
	return true
}

// Ordinary data loss is a reason to re-confirm, not proof that MTU shrank.
func (f *mtuFinder) requestConfirmation() {
	if f.target != 0 && f.min >= f.target {
		f.confirmUrgent = true
	}
}

// One ordinary congestion-accounted packet after the target probe provides
// a higher acknowledged packet number. ACK-driven loss detection then owns
// the actual time threshold, including RTT changes and reordering allowance.
func (f *mtuFinder) needsBaseFeedback() bool {
	return f.confirmUrgent && f.inFlight != protocol.InvalidByteCount && !f.baseFeedbackSent
}
func (f *mtuFinder) nextUrgentProbeTime() monotime.Time {
	if !f.confirmUrgent || f.inFlight != protocol.InvalidByteCount || f.lastProbeTime.IsZero() {
		return 0
	}
	return f.lastProbeTime.Add(f.targetDelay())
}
