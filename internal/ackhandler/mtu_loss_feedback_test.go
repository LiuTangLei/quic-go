package ackhandler

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestMTULossFeedbackUsesOnlyOrdinaryOneRTTPackets(t *testing.T) {
	for _, tc := range []struct {
		name               string
		level              protocol.EncryptionLevel
		mtu, path, ackOnly bool
		want               protocol.ByteCount
	}{
		{name: "ordinary", level: protocol.Encryption1RTT, want: 1320},
		{name: "mtu", level: protocol.Encryption1RTT, mtu: true},
		{name: "path", level: protocol.Encryption1RTT, path: true},
		{name: "ack-only", level: protocol.Encryption1RTT, ackOnly: true},
		{name: "initial", level: protocol.EncryptionInitial},
		{name: "handshake", level: protocol.EncryptionHandshake},
		{name: "early-data", level: protocol.Encryption0RTT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rtt := utils.NewRTTStats()
			rtt.UpdateRTT(20*time.Millisecond, 0)
			h := NewSentPacketHandler(0, 1200, rtt, &utils.ConnectionStats{}, true, false, nil, protocol.PerspectiveServer, nil, utils.DefaultLogger).(*sentPacketHandler)
			now := monotime.Now()
			var tracker packetTracker
			for _, size := range []protocol.ByteCount{1320, 1250} {
				pn := h.PopPacketNumber(tc.level.PacketNumberSpace())
				var frames []Frame
				if !tc.ackOnly {
					frames = []Frame{tracker.NewPingFrame(pn)}
				}
				h.SentPacket(now.Add(-time.Second), pn, protocol.InvalidPacketNumber, nil, frames, tc.level, protocol.ECNNon, size, tc.mtu, tc.path)
			}
			// A newer acknowledged packet establishes the ordinary time-loss boundary.
			pn := h.PopPacketNumber(tc.level.PacketNumberSpace())
			h.SentPacket(now, pn, protocol.InvalidPacketNumber, nil, nil, tc.level, protocol.ECNNon, 100, false, false)
			h.getPacketNumberSpace(tc.level.PacketNumberSpace()).largestAcked = pn
			h.detectLostPackets(now, tc.level)
			require.Equal(t, tc.want, h.TakeLostDataPacketSize())
			require.Zero(t, h.TakeLostDataPacketSize(), "feedback is read and cleared, with no retained packet ownership")
			if tc.want > 0 {
				require.Len(t, tracker.Lost, 2, "normal loss callbacks still run once for each packet")
			}
		})
	}
}

func TestMTULossFeedbackMigrationAndRetryReset(t *testing.T) {
	h := NewSentPacketHandler(0, 1200, utils.NewRTTStats(), &utils.ConnectionStats{}, true, false, nil, protocol.PerspectiveClient, nil, utils.DefaultLogger).(*sentPacketHandler)
	h.lostDataPacketSize = 1320
	h.ResetForRetry(monotime.Now())
	require.Zero(t, h.TakeLostDataPacketSize())
	h.lostDataPacketSize = 1320
	h.MigratedPath(monotime.Now(), 1200)
	require.Zero(t, h.TakeLostDataPacketSize())
}
