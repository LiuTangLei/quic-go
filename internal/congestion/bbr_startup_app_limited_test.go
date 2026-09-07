package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func startupWithBandwidth() (*bbrSender, *mockClock) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1200)
	b.minRtt = 60 * time.Millisecond
	b.minRttTimestamp = clock.Now()
	b.maxBandwidth.Reset(20_000_000, 1) // 150 kB BDP
	return b, &clock
}

func TestBBRStartupChunkBoundaryDoesNotMarkFullPipeAppLimited(t *testing.T) {
	b, _ := startupWithBandwidth()
	b.congestionWindow = b.GetTargetCongestionWindow(2)
	flight := b.GetTargetCongestionWindow(1.5)
	require.Less(t, flight, b.GetCongestionWindow(), "fixture must not be cwnd-limited")
	b.OnApplicationLimited(flight)
	require.False(t, b.sampler.isAppLimited, "a transient empty write queue must not invalidate a full pipe")
	require.Zero(t, b.connStats.ApplicationLimitedRTTSamples.Load())

	b.OnApplicationLimited(b.GetTargetCongestionWindow(1))
	require.True(t, b.sampler.isAppLimited, "a producer unable to keep the pipe full must still be marked")
	unknown := NewBBRSender(b.clock, utils.NewRTTStats(), 1200)
	unknown.OnApplicationLimited(unknown.GetCongestionWindow() - 1200)
	require.True(t, unknown.sampler.isAppLimited, "do not infer pipe fullness without a bandwidth sample")
}

func TestBBRStartupRoundCanUseLaterUnrestrictedACK(t *testing.T) {
	b, clock := startupWithBandwidth()
	var pn protocol.PacketNumber
	var flight protocol.ByteCount
	send := func() protocol.PacketNumber {
		pn++
		flight += 1200
		b.OnPacketSent(clock.Now(), flight, pn, 1200, true)
		return pn
	}
	ack := func(p protocol.PacketNumber) {
		b.OnPacketAcked(p, 1200, flight, clock.Now())
		flight -= 1200
	}
	// Construct actual sampler transitions: the marker is at packet 10, the
	// first round ends at 20, and ACK 11 clears the marker before packet 22 is
	// sent. ACK 21 then starts a round with an app-limited sample, while ACK 22
	// provides an unrestricted sample within that same round.
	for cycle := 0; cycle < 4 && !b.isAtFullBandwidth; cycle++ {
		base := pn
		for range 10 {
			send()
		}
		b.OnApplicationLimited(flight)
		for range 10 {
			send()
		}
		clock.Advance(60 * time.Millisecond)
		ack(base + 1)
		send() // packet 21 is still app-limited
		ack(base + 11)
		require.False(t, b.sampler.isAppLimited)
		send() // packet 22 records unrestricted send state
		clock.Advance(60 * time.Millisecond)
		before := b.roundsWithoutBandwidthGain
		ack(base + 21)
		require.True(t, b.lastSampleIsAppLimited)
		require.Equal(t, before, b.roundsWithoutBandwidthGain, "app-limited boundary cannot count a plateau")
		round := b.roundTripCount
		ack(base + 22)
		require.False(t, b.lastSampleIsAppLimited)
		require.Equal(t, round, b.roundTripCount)
		require.Equal(t, before+1, b.roundsWithoutBandwidthGain, "a later unrestricted ACK must consume this round's check")
		ack(base + 2) // another unrestricted ACK, reordered within this round
		require.Equal(t, before+1, b.roundsWithoutBandwidthGain, "one round cannot count twice")
	}
	require.True(t, b.isAtFullBandwidth, "saturated delivery must eventually leave STARTUP")
}

func TestBBRStartupPureAppLimitedRoundsDoNotDeclareFullBandwidth(t *testing.T) {
	b, clock := startupWithBandwidth()
	for pn := protocol.PacketNumber(1); pn <= 40; pn++ {
		b.OnApplicationLimited(0)
		b.OnPacketSent(clock.Now(), 100, pn, 100, true)
		clock.Advance(60 * time.Millisecond)
		b.OnPacketAcked(pn, 100, 100, clock.Now())
	}
	require.Greater(t, b.roundTripCount, int64(BandwidthWindowSize))
	require.Zero(t, b.roundsWithoutBandwidthGain)
	require.False(t, b.isAtFullBandwidth)
	require.True(t, b.InSlowStart())
	require.Equal(t, Bandwidth(20_000_000), b.BandwidthEstimate(), "small reverse packets must not replace the model")
}

func TestBBRStartupProbeRTTDoesNotReuseRoundCheck(t *testing.T) {
	b, clock := startupWithBandwidth()
	b.fullBandwidthCheckPending = true
	b.MaybeEnterOrExitProbeRtt(clock.Now(), false, true)
	require.False(t, b.fullBandwidthCheckPending)
	b.fullBandwidthCheckPending = true
	b.EnterStartupMode(clock.Now())
	require.False(t, b.fullBandwidthCheckPending, "returning from ProbeRTT requires a new round")
}
