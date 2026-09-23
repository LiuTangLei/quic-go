package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
)

// Exercise the sender, not only the sampler: taking a sent-state once must
// preserve byte accounting and retirement under reordered/duplicate events.
func TestBBRv3ACKStateTakenExactlyOnce(t *testing.T) {
	b, clock := newTestBBRv3()
	const count = 96
	var inFlight, acked, lost protocol.ByteCount
	for pn := protocol.PacketNumber(0); pn < count; pn++ {
		size := protocol.ByteCount(128 + int(pn)%8*100)
		inFlight += size
		b.OnPacketSent(clock.Now(), inFlight, pn, size, true)
	}
	*clock = mockClock(clock.Now().Add(50 * time.Millisecond))
	for pn := protocol.PacketNumber(count - 1); pn >= 0; pn-- {
		size := protocol.ByteCount(128 + int(pn)%8*100)
		switch pn % 3 {
		case 0:
			b.OnPacketAcked(pn, size, inFlight, clock.Now())
			b.OnPacketAcked(pn, size, inFlight, clock.Now()) // duplicate
			acked += size
		case 1:
			b.OnCongestionEvent(pn, size, inFlight)
			b.OnCongestionEvent(pn, size, inFlight)          // duplicate
			b.OnPacketAcked(pn, size, inFlight, clock.Now()) // late ACK
			lost += size
		case 2:
			b.OnPacketDiscarded(pn)
			b.OnPacketAcked(pn, size, inFlight, clock.Now())
			b.OnCongestionEvent(pn, size, inFlight)
		}
		inFlight -= size
		if b.sampler.totalBytesAcked != acked || b.sampler.totalBytesLost != lost {
			t.Fatalf("packet %d: acked=%d/%d lost=%d/%d", pn,
				b.sampler.totalBytesAcked, acked, b.sampler.totalBytesLost, lost)
		}
	}
	if len(b.sent) != 0 || len(b.sampler.connectionStats.stats) != 0 {
		t.Fatal("ACK/loss/discard left sent-packet records behind")
	}
}

func TestBBRv3InvalidTimestampStillAcknowledgesOnce(t *testing.T) {
	b, clock := newTestBBRv3()
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	// Zero elapsed time cannot produce a bandwidth sample. It still confirms
	// delivery and must remove the packet without changing the path-rate model.
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	if b.sampler.totalBytesAcked != 1200 || b.BandwidthEstimate() != 0 {
		t.Fatal("invalid timestamp lost delivery accounting or inflated bandwidth")
	}
	if len(b.sent) != 0 || len(b.sampler.connectionStats.stats) != 0 {
		t.Fatal("invalid sample leaked its sent state")
	}
}
