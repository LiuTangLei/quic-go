package congestion

import (
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"testing"
	"time"
)

func TestBBRSmallReverseACKsDoNotReplaceBandwidthEstimate(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1200)
	b.maxBandwidth.Update(300_000_000, 0)
	b.isAtFullBandwidth = true
	b.mode = PROBE_BW
	b.minRtt = 60 * time.Millisecond
	b.minRttTimestamp = clock.Now()
	b.OnApplicationLimited(b.GetCongestionWindow())
	if b.sampler.isAppLimited {
		t.Fatal("cwnd-limited sender labeled application-limited")
	}
	for pn := protocol.PacketNumber(1); pn <= 40; pn++ {
		b.OnApplicationLimited(0)
		b.OnPacketSent(clock.Now(), 100, pn, 100, true)
		clock.Advance(60 * time.Millisecond)
		b.OnPacketAcked(pn, 100, 100, clock.Now())
	}
	if b.BandwidthEstimate() != 300_000_000 {
		t.Fatalf("reverse ACKs overwrote bandwidth: %d", b.BandwidthEstimate())
	}
	if b.roundTripCount < 10 {
		t.Fatal("fixture did not outlast bandwidth-filter window")
	}
}
