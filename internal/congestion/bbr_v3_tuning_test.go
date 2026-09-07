package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func establishedBBR(t *testing.T) (*bbrSender, *mockClock) {
	t.Helper()
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1200)
	b.isAtFullBandwidth = true
	b.mode = PROBE_BW
	b.minRtt = 50 * time.Millisecond
	b.minRttTimestamp = clock.Now()
	b.roundTripCount = 1
	b.maxBandwidth.Update(80_000_000, 1) // 500 kB BDP
	b.congestionWindow = 1_000_000
	b.cycleCurrentOffset = 0
	b.pacingGain = PacingGain[0]
	b.lastCycleStart = clock.Now()
	b.CalculatePacingRate()
	return b, &clock
}

func TestBBRProbeBandwidthLossBelowTarget(t *testing.T) {
	b, clock := establishedBBR(t)
	b.OnPacketSent(clock.Now(), 100_000, 1, 1200, true)
	clock.Advance(60 * time.Millisecond)
	b.OnCongestionEvent(1, 1200, 100_000)
	require.Less(t, b.pacingGain, 1.0, "loss must end the high-gain probe even below target flight")
	require.Less(t, b.pacingRate, b.BandwidthEstimate())
}

func TestBBRProbeBandwidthRemembersEarlyLoss(t *testing.T) {
	b, clock := establishedBBR(t)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	b.OnPacketSent(clock.Now(), 2400, 2, 1200, true)
	clock.Advance(10 * time.Millisecond)
	b.OnCongestionEvent(1, 1200, 2400)
	clock.Advance(50 * time.Millisecond)
	b.OnPacketAcked(2, 1200, 1200, clock.Now())
	require.Less(t, b.pacingGain, 1.0, "ACK must remember loss earlier in this probe")
}

func TestBBRBatchLossDoesNotSkipDrain(t *testing.T) {
	b, clock := establishedBBR(t)
	for pn := protocol.PacketNumber(1); pn <= 10; pn++ {
		b.OnPacketSent(clock.Now(), protocol.ByteCount(pn)*1200, pn, 1200, true)
	}
	clock.Advance(60 * time.Millisecond)
	for pn := protocol.PacketNumber(1); pn <= 5; pn++ {
		b.OnCongestionEvent(pn, 1200, 12_000)
	}
	require.Less(t, b.pacingGain, 1.0, "one ACK's losses must not advance multiple phases")
	require.EqualValues(t, 5, b.connStats.PacketsLost.Load())
}

func TestBBRECNDoesNotCountAsPacketLoss(t *testing.T) {
	b, clock := establishedBBR(t)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	clock.Advance(60 * time.Millisecond)
	b.OnCongestionEvent(1, 0, 1200)
	require.Zero(t, b.connStats.PacketsLost.Load())
	require.Zero(t, b.connStats.BytesLost.Load())
	require.Zero(t, b.sampler.totalBytesLost)
	require.NotNil(t, b.sampler.connectionStats.Get(1))
	require.True(t, b.InRecovery(), "ECN still signals congestion")
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	require.Equal(t, protocol.ByteCount(1200), b.sampler.totalBytesAcked)
}

func TestBBRAckAggregationLongGapAndBurstBound(t *testing.T) {
	b, clock := establishedBBR(t)
	b.maxBandwidth.Reset(1_000_000_000, 0)
	b.aggregationEpochStartTime = clock.Now()
	b.aggregationEpochBytes = 1200
	clock.Advance(120 * time.Second)
	require.Zero(t, b.UpdateAckAggregationBytes(clock.Now(), 1200), "idle time must not overflow expected delivery")
	for range 2000 {
		b.UpdateAckAggregationBytes(clock.Now(), 1200)
	}
	require.LessOrEqual(t, b.maxAckHeight.GetBest(), int64(b.congestionWindow))
}

func TestBBRIdleRestartResetsAggregationAndProbeGain(t *testing.T) {
	b, clock := establishedBBR(t)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	clock.Advance(50 * time.Millisecond)
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	b.aggregationEpochBytes = 1200
	clock.Advance(120 * time.Second)
	b.OnPacketSent(clock.Now(), 1200, 2, 1200, true)
	require.Equal(t, clock.Now(), b.aggregationEpochStartTime)
	require.Zero(t, b.aggregationEpochBytes)
	require.LessOrEqual(t, b.pacingRate, b.BandwidthEstimate(), "restart should not burst with the old probe gain")
	require.True(t, b.sampler.isAppLimited)
	require.Equal(t, Bandwidth(80_000_000), b.BandwidthEstimate())
}

func TestBBRProbeRTTUsesUnqueuedBDPAndFreshRound(t *testing.T) {
	b, clock := establishedBBR(t)
	clock.Advance(MinRttExpiry + time.Second)
	require.True(t, b.updateMinRtt(clock.Now(), 400*time.Millisecond))
	require.Equal(t, 50*time.Millisecond, b.minRtt, "queued expiry sample must not inflate the drain target")
	b.lastSendPacket = 100
	b.currentRoundTripEnd = 10
	b.bytesInFlight = 250_000
	b.MaybeEnterOrExitProbeRtt(clock.Now(), false, true)
	require.Equal(t, bbrMode(PROBE_RTT), b.mode)
	require.Equal(t, protocol.ByteCount(250_000), b.GetCongestionWindow())
	require.Equal(t, protocol.PacketNumber(100), b.currentRoundTripEnd)
	require.False(t, b.UpdateRoundTripCounter(11), "pre-drain packets cannot complete the probe round")
	require.Equal(t, 50*time.Millisecond, b.minRtt)
	clock.Advance(ProbeRttTime + time.Millisecond)
	b.updateMinRtt(clock.Now(), 80*time.Millisecond) // genuine path RTT increase
	b.MaybeEnterOrExitProbeRtt(clock.Now(), true, false)
	require.Equal(t, bbrMode(PROBE_BW), b.mode)
	require.Equal(t, 80*time.Millisecond, b.minRtt, "a completed probe can learn a higher path RTT")
}

func TestBBRProbeRTTPacingFallsBeforeFullBandwidth(t *testing.T) {
	b, _ := establishedBBR(t)
	b.isAtFullBandwidth = false
	b.mode = PROBE_RTT
	b.pacingGain = 1
	b.pacingRate = 3 * b.BandwidthEstimate()
	b.CalculatePacingRate()
	require.LessOrEqual(t, b.pacingRate, b.BandwidthEstimate())
}

func TestBBRTargetWindowHighBDPDoesNotOverflow(t *testing.T) {
	b, _ := establishedBBR(t)
	b.maxBandwidth.Reset(100_000_000_000, 0) // 100 Gbps, 1 second RTT
	b.minRtt = time.Second
	require.Equal(t, b.maxCongestionWindow, b.GetTargetCongestionWindow(2))
	require.Equal(t, b.maxCongestionWindow, b.GetTargetCongestionWindow(0.5))
}

func TestBBRStartupAndProbeRTTWindowTargets(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1250)
	b.maxBandwidth.Update(80_000_000, 1)
	b.minRtt = 50 * time.Millisecond
	require.Equal(t, protocol.ByteCount(1_000_000), b.GetTargetCongestionWindow(b.congestionWindowGain))
	b.mode = PROBE_RTT
	require.Equal(t, protocol.ByteCount(250_000), b.GetCongestionWindow())
	b.maxBandwidth.Reset(1000, 1)
	b.SetMaxDatagramSize(1400)
	require.Equal(t, protocol.ByteCount(5600), b.GetCongestionWindow(), "low BDP retains four actual datagrams")
}
