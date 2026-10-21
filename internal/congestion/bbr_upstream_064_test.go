package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

// Upstream 0.64 can fall back to 1200 on EMSGSIZE before PMTU discovery.
// This is a size adjustment on the same path, not a migration/controller reset.
func TestBBRInitialPacketSizeFallback(t *testing.T) {
	clock := mockClock(time.Second)
	for _, sender := range []SendAlgorithmWithDebugInfos{
		NewBBRSender(&clock, utils.NewRTTStats(), 1452),
		NewBBRv3Sender(&clock, utils.NewRTTStats(), 1452),
	} {
		require.NotPanics(t, func() { sender.SetMaxDatagramSize(1200) })
		require.Equal(t, protocol.ByteCount(32*1200), sender.GetCongestionWindow())
		require.True(t, sender.InSlowStart())
		require.False(t, sender.CanSend(sender.GetCongestionWindow()))
	}
}

func TestBBRv3SingleSpuriousPacketDoesNotUndoEpisode(t *testing.T) {
	b, _ := newTestBBRv3()
	seedBBRv3Path(b)
	b.saveUndo()
	b.inRecovery = true
	b.congestionWindow = 7200
	b.inflightLongterm = 9600
	b.OnSpuriousLoss(123)
	require.True(t, b.InRecovery())
	require.Equal(t, protocol.ByteCount(7200), b.GetCongestionWindow())
	require.Equal(t, protocol.ByteCount(9600), b.inflightLongterm)
}
