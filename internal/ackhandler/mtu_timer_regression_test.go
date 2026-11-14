package ackhandler

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestMTUProbeAloneKeepsAcknowledgmentBasedLossTimer(t *testing.T) {
	for _, threshold := range []uint8{defaultTimeThresholdEighths, maxTimeThresholdEighths} {
		t.Run(time.Duration(threshold).String(), func(t *testing.T) {
			rtt := utils.NewRTTStats()
			rtt.UpdateRTT(20*time.Millisecond, 0)
			h := NewSentPacketHandler(0, 1200, rtt, &utils.ConnectionStats{}, true, false, nil, protocol.PerspectiveServer, nil, utils.DefaultLogger).(*sentPacketHandler)
			h.handshakeConfirmed = true
			h.peerCompletedAddressValidation = true
			h.timeThresholdEighths = threshold
			var tracker packetTracker
			now := monotime.Now()
			probe := h.PopPacketNumber(protocol.PacketNumberSpaceAppData)
			h.SentPacket(now, probe, protocol.InvalidPacketNumber, nil, []Frame{tracker.NewPingFrame(probe)}, protocol.Encryption1RTT, protocol.ECNNon, 1320, true, false)
			require.Zero(t, h.GetLossDetectionTimeout(), "an isolated MTU probe must not drive PTO")
			feedback := h.PopPacketNumber(protocol.PacketNumberSpaceAppData)
			h.SentPacket(now.Add(time.Millisecond), feedback, protocol.InvalidPacketNumber, nil, []Frame{tracker.NewPingFrame(feedback)}, protocol.Encryption1RTT, protocol.ECNNon, 1200, false, false)
			_, err := h.ReceivedAck(&wire.AckFrame{AckRanges: ackRanges(feedback)}, protocol.Encryption1RTT, now.Add(21*time.Millisecond))
			require.NoError(t, err)
			require.False(t, h.hist.HasOutstandingPackets(), "only the MTU probe remains")
			require.Empty(t, tracker.Lost)
			expected := now.Add(20 * time.Millisecond * time.Duration(8+threshold) / 8)
			require.Equal(t, expected, h.GetLossDetectionTimeout(), "ACK evidence must retain the exact current loss threshold")
			require.NoError(t, h.OnLossDetectionTimeout(expected))
			require.Equal(t, []protocol.PacketNumber{probe}, tracker.Lost)
			require.Zero(t, h.GetLossDetectionTimeout())
			require.Zero(t, h.ptoCount)
			require.Zero(t, h.numProbesToSend)
			require.Zero(t, h.TakeLostDataPacketSize(), "MTU loss is excluded from ordinary data feedback")
			require.Zero(t, h.connStats.PacketsLost.Load(), "MTU failure does not become a BBR congestion loss")
		})
	}
}
