//go:build darwin || linux

package quic

import (
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go/internal/ackhandler"
	mockackhandler "github.com/quic-go/quic-go/internal/mocks/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/quic-go/quic-go/qlog"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMTUUrgentConfirmationPreservesThreeLossRule(t *testing.T) {
	f, now := promotedWriteErrorFinder(t)
	quick := 5 * f.rttStats.SmoothedRTT()
	f.requestConfirmation()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
	require.False(t, f.takeReverted())
	require.Equal(t, now.Add(quick), f.nextUrgentProbeTime())
	require.False(t, f.ShouldSendProbe(now.Add(quick-time.Nanosecond)))
	for i := range maxLostMTUProbes {
		now = now.Add(quick)
		_, lost := probeTarget(t, f, now)
		require.True(t, f.needsBaseFeedback())
		require.Zero(t, f.nextUrgentProbeTime())
		now = now.Add(quick)
		require.True(t, f.needsBaseFeedback())
		f.baseFeedbackSent = true
		require.False(t, f.needsBaseFeedback())
		require.Zero(t, f.nextUrgentProbeTime())
		lost()
		if i < maxLostMTUProbes-1 {
			require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
			require.False(t, f.takeReverted())
		}
	}
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	require.True(t, f.takeReverted())
	require.False(t, f.confirmUrgent)
	require.Zero(t, f.nextUrgentProbeTime())
	require.False(t, f.ShouldSendProbe(f.lastProbeTime.Add(mtuTargetRetryInterval-time.Nanosecond)))
}

func TestMTUUrgentConfirmationAckAndResetClearWork(t *testing.T) {
	f, now := promotedWriteErrorFinder(t)
	f.requestConfirmation()
	now = now.Add(5 * f.rttStats.SmoothedRTT())
	ack, _ := probeTarget(t, f, now)
	ack()
	require.False(t, f.confirmUrgent)
	require.Zero(t, f.nextUrgentProbeTime())
	require.False(t, f.ShouldSendProbe(now.Add(time.Second)))
	f.requestConfirmation()
	now = now.Add(5 * f.rttStats.SmoothedRTT())
	ack, lost := probeTarget(t, f, now)
	f.Reset(1200, 1452)
	ack()
	lost()
	require.False(t, f.confirmUrgent)
	require.False(t, f.needsBaseFeedback())
	require.Zero(t, f.nextUrgentProbeTime())
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
}

func TestMTUUrgentOnlyLargerOrdinaryLossRequestsConfirmation(t *testing.T) {
	for _, size := range []protocol.ByteCount{0, 1199, 1200, 1201, 1320} {
		t.Run(strconv.Itoa(int(size)), func(t *testing.T) {
			f, _ := promotedWriteErrorFinder(t)
			h := mockackhandler.NewMockSentPacketHandler(gomock.NewController(t))
			c := &Conn{handshakeConfirmed: true, mtuDiscoverer: f, sentPacketHandler: h}
			h.EXPECT().TakeLostDataPacketSize().Return(size)
			c.handleMTULossFeedback()
			require.Equal(t, size > 1200, f.confirmUrgent)
			require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
			require.Zero(t, f.targetLost)
		})
	}
}

func TestPackPTOProbeTemporaryBudgetRetainsDatagramFIFO(t *testing.T) {
	ctrl := gomock.NewController(t)
	tp := newTestPacketPacker(t, ctrl, protocol.PerspectiveServer)
	tp.sealingManager.EXPECT().Get1RTTSealer().Return(newMockShortHeaderSealer(ctrl), nil).Times(2)
	tp.ackFramer.EXPECT().GetAckFrame(protocol.Encryption1RTT, gomock.Any(), gomock.Any()).Times(2)
	tp.pnManager.EXPECT().PeekPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(42), protocol.PacketNumberLen2).Times(2)
	tp.pnManager.EXPECT().PopPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(42)).Times(2)
	tp.framer.EXPECT().HasData().Times(2)
	large := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 1240)}
	small := &wire.DatagramFrame{DataLenPresent: true, Data: []byte{7, 8, 9}}
	require.NoError(t, tp.datagramQueue.Add(large))
	require.NoError(t, tp.datagramQueue.Add(small))
	p, err := tp.packer.PackPTOProbePacket(protocol.Encryption1RTT, 1200, true, false, monotime.Now(), protocol.Version1)
	require.NoError(t, err)
	require.NotNil(t, p)
	require.LessOrEqual(t, p.buffer.Len(), protocol.ByteCount(1200))
	require.Len(t, p.shortHdrPacket.Frames, 1)
	require.IsType(t, &wire.PingFrame{}, p.shortHdrPacket.Frames[0].Frame)
	require.Same(t, large, tp.datagramQueue.Peek())
	require.Empty(t, tp.Recorder.Events(qlog.DatagramDropped{}))
	p.buffer.Release()
	// A normal packet again has the full path budget, and sends the originals
	// in FIFO order, once each. The PTO did not manufacture application replay.
	p, err = tp.packer.PackPTOProbePacket(protocol.Encryption1RTT, 1320, false, true, monotime.Now(), protocol.Version1)
	require.NoError(t, err)
	require.NotNil(t, p)
	require.Len(t, p.shortHdrPacket.Frames, 2)
	require.Same(t, large, p.shortHdrPacket.Frames[0].Frame)
	require.Same(t, small, p.shortHdrPacket.Frames[1].Frame)
	require.Nil(t, tp.datagramQueue.Peek())
	p.buffer.Release()
}

func TestMTUUrgentBaseFeedbackIsNormallyAccounted(t *testing.T) {
	ctrl := gomock.NewController(t)
	h := mockackhandler.NewMockSentPacketHandler(ctrl)
	sender := NewMockSender(ctrl)
	tc := newServerTestConnection(t, ctrl, nil, false, connectionOptHandshakeConfirmed(), connectionOptSentPacketHandler(h), connectionOptSender(sender))
	f, now := promotedWriteErrorFinder(t)
	tc.conn.mtuDiscoverer = f
	tc.conn.peerParams = &wire.TransportParameters{}
	f.requestConfirmation()
	now = now.Add(5 * f.rttStats.SmoothedRTT())
	probeTarget(t, f, now)
	now = now.Add(5 * f.rttStats.SmoothedRTT())
	b := getPacketBuffer()
	b.Data = b.Data[:1200]
	tc.packer.EXPECT().PackMTUProbePacket(gomock.Any(), protocol.ByteCount(1200), protocol.Version1).DoAndReturn(func(ping ackhandler.Frame, _ protocol.ByteCount, _ protocol.Version) (shortHeaderPacket, *packetBuffer, error) {
		require.IsType(t, &wire.PingFrame{}, ping.Frame)
		return shortHeaderPacket{PacketNumber: 42, Length: 1200, Frames: []ackhandler.Frame{ping}, IsPathMTUProbePacket: true}, b, nil
	})
	h.EXPECT().SendMode(now).Return(ackhandler.SendAny)
	h.EXPECT().ECNMode(true).Return(protocol.ECNNon)
	h.EXPECT().SentPacket(now, protocol.PacketNumber(42), protocol.InvalidPacketNumber, gomock.Any(), gomock.Any(), protocol.Encryption1RTT, protocol.ECNNon, protocol.ByteCount(1200), false, false)
	sender.EXPECT().Send(b, uint16(0), protocol.ECNNon).Do(func(buf *packetBuffer, _ uint16, _ protocol.ECN) { buf.Release() })
	require.NoError(t, tc.conn.triggerSending(now))
	require.True(t, f.baseFeedbackSent)
}

func TestMTUUrgentBaseFeedbackHonorsCongestionAndPacing(t *testing.T) {
	for _, mode := range []ackhandler.SendMode{ackhandler.SendNone, ackhandler.SendAck, ackhandler.SendPacingLimited} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			h := mockackhandler.NewMockSentPacketHandler(ctrl)
			sender := NewMockSender(ctrl)
			tc := newServerTestConnection(t, ctrl, nil, false, connectionOptHandshakeConfirmed(), connectionOptSentPacketHandler(h), connectionOptSender(sender))
			f, now := promotedWriteErrorFinder(t)
			tc.conn.mtuDiscoverer = f
			tc.conn.peerParams = &wire.TransportParameters{}
			f.requestConfirmation()
			now = now.Add(5 * f.rttStats.SmoothedRTT())
			probeTarget(t, f, now)
			now = now.Add(5 * f.rttStats.SmoothedRTT())
			h.EXPECT().SendMode(now).Return(mode)
			if mode != ackhandler.SendNone {
				h.EXPECT().ECNMode(true).Return(protocol.ECNNon)
				tc.packer.EXPECT().PackAckOnlyPacket(protocol.ByteCount(1320), now, protocol.Version1).Return(shortHeaderPacket{}, nil, errNothingToPack)
			}
			if mode == ackhandler.SendPacingLimited {
				h.EXPECT().TimeUntilSend().Return(now.Add(time.Second))
			}
			// There are no expectations for PackMTUProbePacket, SentPacket or Send:
			// an ordinary feedback packet must wait just like application traffic.
			require.NoError(t, tc.conn.triggerSending(now))
			require.False(t, f.baseFeedbackSent)
		})
	}
}

func TestMTUUrgentTimerDoesNotPreemptPacing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctrl := gomock.NewController(t)
		h := mockackhandler.NewMockSentPacketHandler(ctrl)
		tc := newServerTestConnection(t, ctrl, nil, false, connectionOptHandshakeConfirmed(), connectionOptSentPacketHandler(h))
		f, _ := promotedWriteErrorFinder(t)
		tc.conn.mtuDiscoverer = f
		tc.conn.peerParams = &wire.TransportParameters{}
		f.lastProbeTime = monotime.Now()
		f.requestConfirmation()
		h.EXPECT().GetLossDetectionTimeout().Return(monotime.Time(0))
		tc.conn.pacingDeadline = monotime.Now().Add(250 * time.Millisecond)
		tc.conn.timer = time.NewTimer(time.Hour)
		defer tc.conn.timer.Stop()
		tc.conn.maybeResetTimer()
		time.Sleep(100 * time.Millisecond)
		select {
		case <-tc.conn.timer.C:
			t.Fatal("urgent MTU timer fired before pacing allowed sending")
		default:
		}
		time.Sleep(150 * time.Millisecond)
		select {
		case <-tc.conn.timer.C:
		default:
			t.Fatal("pacing deadline was not armed")
		}
	})
}

func TestMTUUrgentExpiredDeadlineDoesNotSpinWhileBlocked(t *testing.T) {
	for _, blocked := range []blockMode{blockModeHardBlocked, blockModeCongestionLimited} {
		t.Run(strconv.Itoa(int(blocked)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				h := mockackhandler.NewMockSentPacketHandler(ctrl)
				tc := newServerTestConnection(t, ctrl, nil, false, connectionOptHandshakeConfirmed(), connectionOptSentPacketHandler(h))
				f, _ := promotedWriteErrorFinder(t)
				tc.conn.mtuDiscoverer = f
				f.lastProbeTime = monotime.Now().Add(-time.Second)
				f.requestConfirmation()
				tc.conn.blocked = blocked
				tc.conn.idleTimeout = time.Hour
				tc.conn.lastPacketReceivedTime = monotime.Now()
				if blocked != blockModeHardBlocked {
					h.EXPECT().GetLossDetectionTimeout().Return(monotime.Time(0))
				}
				tc.conn.timer = time.NewTimer(time.Hour)
				defer tc.conn.timer.Stop()
				tc.conn.maybeResetTimer()
				time.Sleep(time.Second)
				select {
				case <-tc.conn.timer.C:
					t.Fatal("past urgent deadline woke a blocked sender")
				default:
				}
			})
		})
	}
}

func TestPackPTOTemporaryBudgetStillSplitsReliableFrames(t *testing.T) {
	ctrl := gomock.NewController(t)
	tp := newTestPacketPacker(t, ctrl, protocol.PerspectiveServer)
	tp.sealingManager.EXPECT().Get1RTTSealer().Return(newMockShortHeaderSealer(ctrl), nil)
	tp.ackFramer.EXPECT().GetAckFrame(protocol.Encryption1RTT, gomock.Any(), false)
	tp.pnManager.EXPECT().PeekPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(42), protocol.PacketNumberLen2)
	tp.pnManager.EXPECT().PopPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(42))
	large := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 1240)}
	require.NoError(t, tp.datagramQueue.Add(large))
	tp.framer.EXPECT().HasData().Return(true)
	tp.framer.EXPECT().Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), protocol.Version1).DoAndReturn(func(cf []ackhandler.Frame, sf []ackhandler.StreamFrame, size protocol.ByteCount, _ monotime.Time, v protocol.Version) ([]ackhandler.Frame, []ackhandler.StreamFrame, protocol.ByteCount) {
		frame, split := (&wire.StreamFrame{Data: make([]byte, 2400)}).MaybeSplitOffFrame(size, v)
		require.True(t, split)
		return cf, append(sf, ackhandler.StreamFrame{Frame: frame}), frame.Length(v)
	})
	p, err := tp.packer.PackPTOProbePacket(protocol.Encryption1RTT, 1200, false, false, monotime.Now(), protocol.Version1)
	require.NoError(t, err)
	require.NotNil(t, p)
	require.Len(t, p.shortHdrPacket.StreamFrames, 1)
	require.Equal(t, protocol.ByteCount(1200), p.buffer.Len())
	require.Same(t, large, tp.datagramQueue.Peek())
	require.Empty(t, tp.Recorder.Events(qlog.DatagramDropped{}))
	p.buffer.Release()
}
