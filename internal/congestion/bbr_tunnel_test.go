package congestion

import (
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"testing"
	"time"
)

func TestBBRTunnelSamplesFirstPacketAndReleasesState(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1250)
	b.OnPacketSent(clock.Now(), 1250, 1, 1250, true) // flight includes this packet
	clock.Advance(50 * time.Millisecond)
	b.OnPacketAcked(1, 1250, 1250, clock.Now())
	if b.BandwidthEstimate() == 0 || b.minRtt != 50*time.Millisecond {
		t.Fatal("first-packet sample lost", b.BandwidthEstimate(), b.minRtt)
	}
	if len(b.sampler.connectionStats.stats) != 0 {
		t.Fatal("acked sample retained")
	}
	b.OnPacketSent(clock.Now(), 1250, 2, 1250, true)
	b.OnPacketDiscarded(2)
	if len(b.sampler.connectionStats.stats) != 0 || b.sampler.totalBytesLost != 0 {
		t.Fatal("discard treated as loss or leaked")
	}
}

func TestBBRTunnelRecoveryBoundaryDoesNotMoveOnAcks(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1250)
	b.lastSendPacket = 10
	b.UpdateRecoveryState(2, true, false)
	if !b.InRecovery() || b.endRecoveryAt != 10 {
		t.Fatal("loss did not enter recovery")
	}
	b.lastSendPacket = 100
	b.UpdateRecoveryState(9, false, true)
	if !b.InRecovery() || b.endRecoveryAt != 10 {
		t.Fatal("ACK moved recovery boundary")
	}
	b.UpdateRecoveryState(11, false, false)
	if b.InRecovery() {
		t.Fatal("recovery stuck while new packets keep being sent")
	}
}

func TestBBRTunnelPacketSizeAndPacingGain(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1250)
	if b.maxDatagramSize != 1250 || b.pacer.maxDatagramSize != 1250 || b.minCongestionWindow != 5000 {
		t.Fatal("wrong initial MTU")
	}
	b.SetMaxDatagramSize(1262)
	if b.pacer.maxDatagramSize != 1262 || b.minCongestionWindow != 5048 {
		t.Fatal("PMTU increase not applied")
	}
	b.pacingRate = Bandwidth(80_000_000)
	if b.pacer.adjustedBandwidth() != 10_000_000 {
		t.Fatal("BBR pacing gain was multiplied again")
	}
	if b.CanSend(b.GetCongestionWindow()) {
		t.Fatal("in-flight bound was bypassed")
	}
	b.OnPacketSent(monotime.Time(time.Second), 1262, 1, 1262, true)
}

func TestBBRTunnelLiveFlightAndLossAccounting(t *testing.T) {
	clock := mockClock(time.Second)
	stats := new(utils.ConnectionStats)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1200, stats)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	stats.BytesInFlight.Store(1200)
	clock.Advance(60 * time.Millisecond)
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	if b.bytesInFlight != 0 {
		t.Fatal("acked packet still in BBR flight")
	}
	b.OnPacketSent(clock.Now(), 1200, 2, 1200, true)
	stats.BytesInFlight.Store(0)
	b.OnCongestionEvent(2, 1200, 1200)
	if stats.PacketsLost.Load() != 1 || stats.BytesLost.Load() != 1200 || b.bytesInFlight != 0 {
		t.Fatal("loss accounting incorrect")
	}
	if len(b.sampler.connectionStats.stats) != 0 {
		t.Fatal("lost sample retained")
	}
	if stats.CongestionWindow.Load() == 0 {
		t.Fatal("missing congestion diagnostic")
	}
	_ = protocol.InitialPacketSize
}
