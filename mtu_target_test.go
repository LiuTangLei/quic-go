package quic

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func newTargetFinder(t *testing.T) (*mtuFinder, monotime.Time) {
	t.Helper()
	rttStats := utils.NewRTTStats()
	rttStats.UpdateRTT(20*time.Millisecond, 0)
	f := newMTUDiscoverer(rttStats, 1200, 1452, nil)
	f.probeOnly(1320)
	now := monotime.Now()
	f.Start(now)
	return f, now
}

// probe sends the next probe at now and reports its size.
func probeTarget(t *testing.T, f *mtuFinder, now monotime.Time) (acked, lost func()) {
	t.Helper()
	require.True(t, f.ShouldSendProbe(now))
	ping, size := f.GetPing(now)
	require.Equal(t, protocol.ByteCount(1320), size, "only the target size is probed")
	require.False(t, f.ShouldSendProbe(now.Add(time.Hour)), "one probe in flight at a time")
	return func() { ping.Handler.OnAcked(ping.Frame) }, func() { ping.Handler.OnLost(ping.Frame) }
}

func TestMTUTargetIsUsedOnceAcknowledged(t *testing.T) {
	f, now := newTargetFinder(t)
	require.False(t, f.ShouldSendProbe(now), "no probe before the usual delay")
	now = now.Add(mtuProbeDelay * 20 * time.Millisecond)
	acked, _ := probeTarget(t, f, now)
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	acked()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
	require.False(t, f.takeReverted())
	// The size is confirmed again later, never searched beyond.
	require.False(t, f.ShouldSendProbe(now.Add(mtuTargetValidateInterval-time.Millisecond)))
	now = now.Add(mtuTargetValidateInterval)
	acked, _ = probeTarget(t, f, now)
	acked()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
}

func TestMTUTargetNotCarriedIsRetriedSlowly(t *testing.T) {
	f, now := newTargetFinder(t)
	quick := mtuProbeDelay * 20 * time.Millisecond
	for range maxLostMTUProbes {
		now = now.Add(quick)
		_, lost := probeTarget(t, f, now)
		lost()
		require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	}
	require.False(t, f.takeReverted(), "nothing in use was withdrawn")
	require.False(t, f.ShouldSendProbe(now.Add(mtuTargetRetryInterval-time.Millisecond)))
	// The path may change under the connection: one probe per interval.
	now = now.Add(mtuTargetRetryInterval)
	acked, _ := probeTarget(t, f, now)
	acked()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
}

func TestMTUTargetIsWithdrawnWhenPathStopsCarryingIt(t *testing.T) {
	f, now := newTargetFinder(t)
	quick := mtuProbeDelay * 20 * time.Millisecond
	now = now.Add(quick)
	acked, _ := probeTarget(t, f, now)
	acked()

	// One lost confirmation is ordinary packet loss; it is re-checked quickly.
	now = now.Add(mtuTargetValidateInterval)
	_, lost := probeTarget(t, f, now)
	lost()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
	now = now.Add(quick)
	acked, _ = probeTarget(t, f, now)
	acked()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())

	// Consecutive losses mean the path changed.
	now = now.Add(mtuTargetValidateInterval)
	for i := range maxLostMTUProbes {
		_, lost := probeTarget(t, f, now)
		lost()
		if i < maxLostMTUProbes-1 {
			require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
			require.False(t, f.takeReverted())
			now = now.Add(quick)
		}
	}
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	require.True(t, f.takeReverted())
	require.False(t, f.takeReverted(), "reported once")
	require.False(t, f.ShouldSendProbe(now.Add(mtuTargetRetryInterval-time.Millisecond)))
	require.True(t, f.ShouldSendProbe(now.Add(mtuTargetRetryInterval)))
}

func TestMTUTargetIgnoresProbesFromBeforeReset(t *testing.T) {
	f, now := newTargetFinder(t)
	now = now.Add(mtuProbeDelay * 20 * time.Millisecond)
	acked, _ := probeTarget(t, f, now)
	f.Reset(1200, 1452)
	acked()
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	require.False(t, f.ShouldSendProbe(now.Add(time.Hour)), "a reset finder waits to be started again")
}
