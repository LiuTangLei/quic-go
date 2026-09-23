package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func newTestBBRv3() (*bbrv3Sender, *mockClock) {
	clock := mockClock(time.Second)
	return NewBBRv3Sender(&clock, utils.NewRTTStats(), 1200), &clock
}
func seedBBRv3Path(b *bbrv3Sender) {
	b.maxBwCurrent = 80_000_000 // 10 MB/s * 50ms = 500kB BDP
	b.minRtt, b.probeRttMinDelay = 50*time.Millisecond, 50*time.Millisecond
	b.hasRTTSample = true
	b.congestionWindow = 1_000_000
	b.fullBandwidthReached = true
	b.setMode(bbrv3ProbeBWCruise)
	b.updateControl(0)
}

func TestBBRv3InitialPolicy(t *testing.T) {
	b, _ := newTestBBRv3()
	require.Equal(t, "bbr-v3", b.ControllerName())
	require.Equal(t, bbrv3Startup, b.mode)
	require.Equal(t, 2.77, b.pacingGain)
	require.Equal(t, 2.0, b.congestionWindowGain)
	require.Equal(t, InfiniteBandwidth, b.bwShortterm)
	require.Equal(t, bbrv3Infinity, b.inflightLongterm)
	require.Equal(t, bbrv3Infinity, b.inflightShortterm)
	require.Positive(t, b.pacingRate)
	require.EqualValues(t, b.pacingRate/BytesPerSecond, b.pacer.adjustedBandwidth())
}

func TestBBRv3BandwidthPlateauAndApplicationLimited(t *testing.T) {
	b, _ := newTestBBRv3()
	b.roundStart = true
	b.checkFullBandwidth(8_000_000, false)
	for i := 0; i < 20; i++ {
		b.checkFullBandwidth(1_000_000, true)
	}
	require.False(t, b.fullBandwidthReached, "application limits must not end probing")
	for i := 0; i < 2; i++ {
		b.checkFullBandwidth(8_000_000, false)
	}
	require.False(t, b.fullBandwidthReached)
	b.checkFullBandwidth(8_000_000, false)
	require.True(t, b.fullBandwidthReached)
	b.enterDrain()
	require.Equal(t, bbrv3Drain, b.mode)
	require.Equal(t, 0.5, b.pacingGain)
}

func TestBBRv3ProbeCycleAndGains(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	b.startProbeDown(clock.Now())
	require.Equal(t, bbrv3ProbeBWDown, b.mode)
	require.Equal(t, .90, b.pacingGain)
	require.GreaterOrEqual(t, b.probeWait, bbrv3ProbeWaitBase)
	require.Less(t, b.probeWait, bbrv3ProbeWaitBase+bbrv3ProbeWaitJitter)
	b.probeWait = 2 * time.Second
	b.roundsSinceProbeUp = 0
	b.bytesInFlight = 400_000
	b.updateProbeBW(clock.Now(), 80_000_000, bbrv3SentPacket{inflight: 400_000}, false, 0, 1200)
	require.Equal(t, bbrv3ProbeBWCruise, b.mode)
	clock.Advance(2100 * time.Millisecond)
	b.updateProbeBW(clock.Now(), 80_000_000, bbrv3SentPacket{inflight: 400_000}, false, 0, 1200)
	require.Equal(t, bbrv3ProbeBWRefill, b.mode)
	b.roundStart = true
	b.updateProbeBW(clock.Now(), 80_000_000, bbrv3SentPacket{inflight: 400_000}, false, 0, 1200)
	require.Equal(t, bbrv3ProbeBWUp, b.mode)
	require.True(t, b.isBWProbeSample)
	require.Equal(t, 1.25, b.pacingGain)
	require.Equal(t, 2.25, b.congestionWindowGain)
	for i := 0; i < 3; i++ {
		b.checkFullBandwidth(80_000_000, false)
	}
	b.updateProbeBW(clock.Now(), 80_000_000, bbrv3SentPacket{inflight: 400_000}, false, 0, 1200)
	require.Equal(t, bbrv3ProbeBWDown, b.mode)
}

func TestBBRv3TwoProbeCycleBandwidthFilter(t *testing.T) {
	b, _ := newTestBBRv3()
	b.maxBwCurrent = 100_000_000
	b.advanceBandwidthFilter()
	b.maxBwCurrent = 10_000_000
	require.EqualValues(t, 100_000_000, b.BandwidthEstimate())
	b.advanceBandwidthFilter()
	require.EqualValues(t, 10_000_000, b.BandwidthEstimate(), "obsolete fast path must age out after another probe cycle")
	b.advanceBandwidthFilter()
	require.EqualValues(t, 10_000_000, b.BandwidthEstimate(), "an empty app-limited cycle must preserve the model")
	require.EqualValues(t, 2, b.cycleCount)
}

func TestBBRv3ShortTermLossBoundsAndRefillReset(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	b.lossInRound = true
	b.bwLatest, b.inflightLatest = 60_000_000, 300_000
	b.adaptShorttermModel()
	require.EqualValues(t, 60_000_000, b.bwShortterm)
	require.EqualValues(t, 700_000, b.inflightShortterm)
	b.bwLatest, b.inflightLatest = 10_000_000, 100_000
	b.adaptShorttermModel()
	require.EqualValues(t, 42_000_000, b.bwShortterm)
	require.EqualValues(t, 490_000, b.inflightShortterm)
	b.updateControl(0)
	require.LessOrEqual(t, b.congestionWindow, protocol.ByteCount(490_000))
	require.Equal(t, bbrv3ScaleBandwidth(42_000_000, .99), b.pacingRate)
	b.startProbeRefill()
	require.Equal(t, InfiniteBandwidth, b.bwShortterm)
	require.Equal(t, bbrv3Infinity, b.inflightShortterm)
}

func TestBBRv3LossThresholdUsesTransmitFlight(t *testing.T) {
	for _, appLimited := range []bool{false, true} {
		t.Run(map[bool]string{false: "loaded", true: "application-limited"}[appLimited], func(t *testing.T) {
			b, clock := newTestBBRv3()
			seedBBRv3Path(b)
			b.setMode(bbrv3ProbeBWUp)
			b.isBWProbeSample = true
			if appLimited {
				b.sampler.OnAppLimited()
			}
			// One 1200-byte loss at send-flight 48kB is >2%, irrespective
			// of the 500kB flight currently in flight at loss detection.
			b.OnPacketSent(clock.Now(), 48_000, 1, 1200, true)
			clock.Advance(50 * time.Millisecond)
			b.OnCongestionEvent(1, 1200, 500_000)
			require.Equal(t, bbrv3ProbeBWDown, b.mode)
			require.False(t, b.isBWProbeSample)
			require.True(t, b.prevProbeTooHigh)
			if appLimited {
				require.Equal(t, bbrv3Infinity, b.inflightLongterm)
			} else {
				require.EqualValues(t, 350_000, b.inflightLongterm, "0.7*target inflight bounds reaction")
			}
			require.Empty(t, b.sent)
			require.Empty(t, b.sampler.connectionStats.stats)
		})
	}
}

func TestBBRv3StartupHighLossRequiresFullRoundAndRanges(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	b.setMode(bbrv3Startup)
	b.fullBandwidthReached = false
	b.inRecovery = true
	b.recoveryStartRound = 3
	b.roundTripCount = 3
	b.lostInRound = 12_000
	b.sampler.totalBytesAcked = 100_000
	b.inflightLatest = 120_000
	b.lossRangesInRound = 6
	b.checkStartupLoss()
	require.False(t, b.fullBandwidthReached)
	b.roundTripCount = 4
	b.lossRangesInRound = 5
	b.checkStartupLoss()
	require.False(t, b.fullBandwidthReached)
	b.lossRangesInRound = 6
	b.checkStartupLoss()
	require.True(t, b.fullBandwidthReached)
	require.EqualValues(t, 500_000, b.inflightLongterm)
}

func TestBBRv3LongTermHeadroomAndSlope(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	b.inflightLongterm = 800_000
	b.updateControl(0)
	require.EqualValues(t, 720_000, b.congestionWindow)
	b.setMode(bbrv3ProbeBWUp)
	b.congestionWindow = 800_000
	b.probeUpAckedPerIncrement = 1200
	b.probeUpRounds = 0
	b.updateProbeBW(clock.Now(), 80_000_000, bbrv3SentPacket{inflight: 800_000, cwndLimited: true}, false, 0, 2400)
	require.EqualValues(t, 802_400, b.inflightLongterm)
}

func TestBBRv3ProbeRTTHalfBDPDurationAndRound(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	require.EqualValues(t, 250_000, b.ProbeRttCongestionWindow())
	clock.Advance(5100 * time.Millisecond)
	b.updateMinRTT(clock.Now(), 70*time.Millisecond)
	require.True(t, b.probeRttExpired)
	b.bytesInFlight = 200_000
	b.roundStart = false
	b.checkProbeRTT(clock.Now(), 1200)
	b.updateControl(1200)
	require.Equal(t, bbrv3ProbeRTT, b.mode)
	require.LessOrEqual(t, b.congestionWindow, protocol.ByteCount(250_000))
	clock.Advance(201 * time.Millisecond)
	b.checkProbeRTT(clock.Now(), 1200)
	require.Equal(t, bbrv3ProbeRTT, b.mode, "wall time alone must not end ProbeRTT")
	b.roundStart = true
	b.checkProbeRTT(clock.Now(), 1200)
	require.Equal(t, bbrv3ProbeBWCruise, b.mode)
	require.GreaterOrEqual(t, b.congestionWindow, protocol.ByteCount(1_000_000))
	require.Equal(t, InfiniteBandwidth, b.bwShortterm)
}

func TestBBRv3IdleRestartPreservesModel(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	b.setMode(bbrv3ProbeBWUp)
	b.updateControl(0)
	cwnd := b.congestionWindow
	b.OnApplicationLimited(0)
	clock.Advance(6 * time.Second)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	require.True(t, b.idleRestart)
	require.Equal(t, bbrv3ScaleBandwidth(80_000_000, .99), b.pacingRate)
	require.Equal(t, cwnd, b.congestionWindow)
	clock.Advance(50 * time.Millisecond)
	b.OnPacketAcked(1, 1200, 1200, clock.Now())
	require.NotEqual(t, bbrv3ProbeRTT, b.mode, "idle restart already drained the queue")
	require.False(t, b.idleRestart)
}

func TestBBRv3AckAggregationWindow(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	b.roundTripCount = 2
	b.updateAckAggregation(clock.Now(), 50_000)
	require.EqualValues(t, 50_000, b.ackAggregationAllowance())
	b.roundTripCount = 11
	require.EqualValues(t, 50_000, b.ackAggregationAllowance())
	b.roundTripCount = 12
	require.Zero(t, b.ackAggregationAllowance())
	b.fullBandwidthReached = false
	b.roundTripCount = 2
	b.roundTripCount = 3
	require.Zero(t, b.ackAggregationAllowance(), "Startup uses only one round of aggregation history")
}

func TestBBRv3PacketLifecycleAndMTU(t *testing.T) {
	b, clock := newTestBBRv3()
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	b.OnPacketSent(clock.Now(), 2400, 2, 1200, true)
	b.OnPacketSent(clock.Now(), 2400, 3, 50, false)
	b.OnPacketDiscarded(1)
	clock.Advance(50 * time.Millisecond)
	b.OnPacketAcked(1, 1200, 2400, clock.Now())
	require.Zero(t, b.sampler.totalBytesAcked)
	b.OnPacketAcked(2, 1200, 1200, clock.Now())
	require.EqualValues(t, 1200, b.sampler.totalBytesAcked)
	b.OnPacketAcked(2, 1200, 1200, clock.Now())
	b.OnCongestionEvent(2, 1200, 1200)
	require.EqualValues(t, 1200, b.sampler.totalBytesAcked)
	require.Zero(t, b.sampler.totalBytesLost)
	require.Empty(t, b.sent)
	require.Empty(t, b.sampler.connectionStats.stats)
	b.SetMaxDatagramSize(1400)
	require.EqualValues(t, 5600, b.minimumCwnd())
	require.EqualValues(t, 1400, b.pacer.maxDatagramSize)
}

func TestBBRv3SpuriousRecoveryRestoresModel(t *testing.T) {
	b, clock := newTestBBRv3()
	seedBBRv3Path(b)
	b.OnPacketSent(clock.Now(), 1200, 1, 1200, true)
	b.OnCongestionEvent(1, 1200, 1200)
	b.bwShortterm = 1_000_000
	b.inflightShortterm = 12_000
	b.inflightLongterm = 24_000
	b.OnSpuriousLossRecovery()
	require.False(t, b.InRecovery())
	require.Equal(t, InfiniteBandwidth, b.bwShortterm)
	require.Equal(t, bbrv3Infinity, b.inflightShortterm)
	require.Equal(t, bbrv3Infinity, b.inflightLongterm)
}

func TestBBRv3TinyBDPAndSaturatingArithmetic(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	b.minRtt = time.Microsecond
	b.maxBwCurrent = 8000
	b.inflightLongterm = 1
	b.inflightShortterm = 1
	b.updateControl(0)
	require.Equal(t, b.minimumCwnd(), b.congestionWindow)
	require.EqualValues(t, 0, bbrv3Volume(1000, -time.Second))
	require.EqualValues(t, 0, bbrv3Rate(1200, 0))
	require.Equal(t, InfiniteBandwidth, bbrv3Rate(bbrv3Infinity, time.Nanosecond))
	require.Equal(t, bbrv3Infinity, bbrv3Volume(InfiniteBandwidth, time.Hour))
	require.Equal(t, bbrv3Infinity, bbrv3AddBytes(bbrv3Infinity-1, 1200))
}
