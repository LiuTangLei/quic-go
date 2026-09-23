package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestBBRv3FullPipeDoesNotBecomeApplicationLimited(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	// A 64 KiB producer temporarily yields while the .99-paced pipe remains
	// full. Marking that yield app-limited suppresses useful bandwidth
	// samples and can stop the two-cycle rate filter from aging down.
	for _, mode := range []bbrv3Mode{bbrv3ProbeBWDown, bbrv3ProbeBWCruise, bbrv3ProbeBWRefill, bbrv3ProbeBWUp} {
		b.setMode(mode)
		b.sampler.isAppLimited = false
		b.OnApplicationLimited(b.bdp(1) * 99 / 100)
		require.False(t, b.sampler.isAppLimited, "full pipe in mode %v", mode)
		b.OnApplicationLimited(b.bdp(1) / 4)
		require.True(t, b.sampler.isAppLimited, "a genuinely starved pipe still needs marking")
	}
}

func TestBBRv3LossToleranceRequiresLowQueueDelay(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	require.Equal(t, bbrv3LossThreshold, b.lossThreshold(), "no RTT evidence")
	b.rttStats.UpdateRTT(50*time.Millisecond, 0)
	require.False(t, b.inflightTooHigh(5000, 100000), "5% with no standing queue")
	require.True(t, b.inflightTooHigh(9000, 100000), "large losses still stop probing")
	b.rttStats.UpdateRTT(100*time.Millisecond, 0)
	require.Equal(t, bbrv3LossThreshold, b.lossThreshold(), "queued path must keep base response")
	require.True(t, b.inflightTooHigh(5000, 100000))
	b.setMode(bbrv3Startup)
	b.fullBandwidthReached = false
	require.Equal(t, bbrv3LossThreshold, b.lossThreshold())
}

func TestBBRv3RandomLossStillReducesShorttermBounds(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	b.rttStats.UpdateRTT(50*time.Millisecond, 0)
	b.lossInRound = true
	b.bwLatest = 60_000_000
	b.inflightLatest = 300000
	b.adaptShorttermModel()
	require.EqualValues(t, 60_000_000, b.bwShortterm)
	require.Equal(t, protocol.ByteCount(700000), b.inflightShortterm)
}
