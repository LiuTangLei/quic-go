package congestion

import (
	"testing"
	"time"
	"unsafe"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
)

func TestSamplerValueSnapshotsAndPacketLifecycle(t *testing.T) {
	// Go stores map values up to 128 bytes inline. A larger future record
	// would silently restore per-packet heap allocation.
	if unsafe.Sizeof(ConnectionStateOnSentPacket{}) > 128 {
		t.Fatal("sent state no longer fits inline in Go map storage")
	}
	s := NewBandwidthSampler()
	now := monotime.Now()
	for pn := protocol.PacketNumber(0); pn < 100; pn++ {
		s.OnPacketSent(now.Add(time.Duration(pn)*time.Microsecond), pn, 1200, protocol.ByteCount(pn)*1200, true)
	}
	old, ok := s.connectionStats.Get(7)
	if !ok || old.size != 1200 {
		t.Fatal("missing sent snapshot")
	}
	if s.connectionStats.Insert(7, now, 9000, s) {
		t.Fatal("duplicate overwrote sent state")
	}
	ackTime := now.Add(100 * time.Millisecond)
	// Reverse ACK order exercises independent lookup/removal instead of
	// advancing a FIFO head past unacknowledged older packets.
	for pn := protocol.PacketNumber(99); pn >= 50; pn-- {
		sample := s.OnPacketAcked(ackTime, pn)
		if !sample.stateAtSend.isValid {
			t.Fatal("valid reordered ACK lost")
		}
	}
	if s.totalBytesAcked != 50*1200 {
		t.Fatal("wrong delivered bytes")
	}
	before := s.totalBytesAcked
	if s.OnPacketAcked(ackTime, 99).stateAtSend.isValid || s.totalBytesAcked != before {
		t.Fatal("duplicate ACK inflated delivery")
	}
	for pn := protocol.PacketNumber(0); pn < 25; pn++ {
		s.OnPacketLost(pn)
	}
	if s.totalBytesLost != 25*1200 {
		t.Fatal("wrong lost bytes")
	}
	if s.OnPacketLost(7).isValid || s.totalBytesLost != 25*1200 {
		t.Fatal("duplicate loss inflated accounting")
	}
	for pn := protocol.PacketNumber(25); pn < 50; pn++ {
		s.connectionStats.Remove(pn)
	}
	if len(s.connectionStats.stats) != 0 {
		t.Fatal("ACK/loss/discard retained packet state")
	}
	if old.size != 1200 || old.sendTime != now.Add(7*time.Microsecond) {
		t.Fatal("retained snapshot mutated")
	}
	// ACK-only packets are not sampled, and an absent ACK is harmless.
	s.OnPacketSent(ackTime, 101, 64, 0, false)
	if len(s.connectionStats.stats) != 0 || s.OnPacketAcked(ackTime, 101).stateAtSend.isValid {
		t.Fatal("ACK-only packet incorrectly sampled")
	}
}

func TestTunedBBRv3RetainsCongestionBoundaries(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	if bbrv3Beta != .7 || bbrv3LossThreshold != .02 || bbrv3UpGain != 1.25 || bbrv3ProbeRTTDuration != 200*time.Millisecond {
		t.Fatal("light tuning must not weaken loss protection or amplify probe gain")
	}
	for range 100 {
		b.startProbeDown(clock.Now())
		if b.probeWait < time.Second || b.probeWait >= 2*time.Second {
			t.Fatal("probe wait outside tuned bounds")
		}
	}
	b.inflightLongterm = 800_000
	if b.inflightWithHeadroom() != 720_000 {
		t.Fatal("ten percent headroom must remain")
	}
	b.setMode(bbrv3ProbeBWUp)
	b.isBWProbeSample = true
	b.handleInflightTooHigh(clock.Now(), 700_000, false)
	if b.mode != bbrv3ProbeBWDown {
		t.Fatal("loss did not stop bandwidth probing")
	}
}
