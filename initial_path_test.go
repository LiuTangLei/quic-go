package quic

import (
	"github.com/quic-go/quic-go/internal/ackhandler"
	mockackhandler "github.com/quic-go/quic-go/internal/mocks/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"testing"
	"time"
)

func TestInitialPathReadyStillObeysPacing(t *testing.T) {
	ctrl := gomock.NewController(t)
	sph := mockackhandler.NewMockSentPacketHandler(ctrl)
	tc := newClientTestConnection(t, ctrl, nil, false, connectionOptSentPacketHandler(sph))
	frame := &wire.CryptoFrame{Data: []byte("outstanding ClientHello")}
	gomock.InOrder(
		sph.EXPECT().QueueProbePacket(protocol.PacketNumberSpaceInitial).DoAndReturn(func(protocol.PacketNumberSpace) bool { tc.conn.retransmissionQueue.addInitial(frame); return true }),
		sph.EXPECT().QueueProbePacket(protocol.PacketNumberSpaceInitial).Return(false),
	)
	tc.conn.handleInitialPathReady()
	require.True(t, tc.conn.retransmissionQueue.HasData(protocol.EncryptionInitial))
	now := monotime.Now()
	deadline := now.Add(time.Second)
	sph.EXPECT().SendMode(now).Return(ackhandler.SendPacingLimited)
	sph.EXPECT().TimeUntilSend().Return(deadline)
	sph.EXPECT().ECNMode(false).Return(protocol.ECNNon)
	// ACK-only packing is allowed, but no retransmission is sent before pacing.
	tc.packer.EXPECT().PackCoalescedPacket(true, gomock.Any(), now, protocol.Version1).Return(nil, nil)
	require.NoError(t, tc.conn.triggerSending(now))
	require.Equal(t, deadline, tc.conn.pacingDeadline)
	require.True(t, tc.conn.retransmissionQueue.HasData(protocol.EncryptionInitial))
}

func TestInitialPathReadyIgnoredAfterReplyOrForServer(t *testing.T) {
	for _, c := range []*Conn{
		{perspective: protocol.PerspectiveServer},
		{perspective: protocol.PerspectiveClient, receivedFirstPacket: true},
		{perspective: protocol.PerspectiveClient, handshakeComplete: true},
	} {
		c.handleInitialPathReady()
	} // nil handler must never be called.
}

func TestInitialPathReadyConfigPreserved(t *testing.T) {
	signal := make(chan struct{})
	c := &Config{InitialPathReady: signal}
	require.Equal(t, (<-chan struct{})(signal), populateConfig(c).InitialPathReady)
	require.Equal(t, (<-chan struct{})(signal), c.Clone().InitialPathReady)
	require.Nil(t, populateConfig(nil).InitialPathReady)
}
