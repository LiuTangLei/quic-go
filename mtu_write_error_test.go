//go:build darwin || linux

package quic

import (
	"fmt"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	mockackhandler "github.com/quic-go/quic-go/internal/mocks/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func promotedWriteErrorFinder(t *testing.T) (*mtuFinder, monotime.Time) {
	f, now := newTargetFinder(t)
	now = now.Add(100 * time.Millisecond)
	ack, _ := probeTarget(t, f, now)
	ack()
	return f, now
}
func TestMTUWriteErrorWithdrawsAndInvalidatesProbe(t *testing.T) {
	f, now := promotedWriteErrorFinder(t)
	now = now.Add(mtuTargetValidateInterval)
	oldAck, oldLoss := probeTarget(t, f, now)
	generation := f.generation
	now = now.Add(time.Millisecond)
	require.True(t, f.withdrawAfterWriteError(1266, now))
	require.Equal(t, generation+1, f.generation)
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	require.True(t, f.takeReverted())
	require.False(t, f.takeReverted())
	oldAck()
	oldLoss()
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	require.Equal(t, maxLostMTUProbes, f.targetLost)
	require.False(t, f.ShouldSendProbe(now.Add(mtuTargetRetryInterval-time.Nanosecond)))
	ack, _ := probeTarget(t, f, now.Add(mtuTargetRetryInterval))
	ack()
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
}
func TestMTUWriteErrorIgnoresUnconfirmedOrBase(t *testing.T) {
	f, now := newTargetFinder(t)
	require.False(t, f.withdrawAfterWriteError(1320, now))
	require.Equal(t, protocol.ByteCount(1200), f.CurrentSize())
	f, now = promotedWriteErrorFinder(t)
	require.False(t, f.withdrawAfterWriteError(1200, now))
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
	f.target = 0
	require.False(t, f.withdrawAfterWriteError(1320, now))
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
}
func TestMTUWriteErrorConnectionOwnerUpdatesOnce(t *testing.T) {
	f, now := promotedWriteErrorFinder(t)
	ctrl := gomock.NewController(t)
	h := mockackhandler.NewMockSentPacketHandler(ctrl)
	c := &Conn{handshakeConfirmed: true, mtuDiscoverer: f, sentPacketHandler: h}
	c.maxPayloadSizeEstimate.Store(uint32(estimateMaxPayloadSize(1320)))
	h.EXPECT().SetMaxDatagramSize(protocol.ByteCount(1200)).Times(1)
	c.handleMTUWriteError(1266, now)
	require.Equal(t, 1200, c.PacketSize())
	c.handleMTUWriteError(1320, now)
	f, now = promotedWriteErrorFinder(t)
	beforeHandshake := &Conn{mtuDiscoverer: f}
	beforeHandshake.handleMTUWriteError(1320, now)
	require.Equal(t, protocol.ByteCount(1320), f.CurrentSize())
}
func TestMTUWriteErrorReadyBatchReportsLargestWithoutReplay(t *testing.T) {
	err := fmt.Errorf("UDP write: %w", syscall.EMSGSIZE)
	conn := &batchTestConn{failure: err}
	q := newSendQueue(conn)
	q.Send(getPacketWithContents(make([]byte, 1200)), 0, protocol.ECNUnsupported)
	q.Send(getPacketWithContents(make([]byte, 1320)), 0, protocol.ECNUnsupported)
	close(q.closeCalled)
	require.NoError(t, q.Run())
	require.Equal(t, 1, conn.batches)
	require.Len(t, conn.packets, 2)
	require.Equal(t, protocol.ByteCount(1320), <-q.tooLarge)
	require.Len(t, q.tooLarge, 0)
	require.Len(t, q.queue, 0)
}
func TestMTUWriteErrorGSOSegmentSizeNotAggregate(t *testing.T) {
	conn := &batchTestConn{failure: syscall.EMSGSIZE}
	q := newSendQueue(conn)
	b := getLargePacketBuffer()
	b.Data = b.Data[:2400]
	q.Send(b, 1200, protocol.ECNNon)
	close(q.closeCalled)
	require.NoError(t, q.Run())
	require.Equal(t, 1, conn.singles)
	require.Equal(t, protocol.ByteCount(1200), <-q.tooLarge)
}
func TestMTUWriteErrorSinglePathAndBoundedShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := NewMockSendConn(gomock.NewController(t))
		conn.EXPECT().Write(gomock.Any(), uint16(0), protocol.ECNUnsupported).Return(syscall.EMSGSIZE).Times(sendQueueCapacity)
		q := newSendQueue(conn)
		for range sendQueueCapacity {
			q.Send(getPacketWithContents(make([]byte, 1320)), 0, protocol.ECNUnsupported)
		}
		done := make(chan error, 1)
		go func() { done <- q.Run() }()
		q.Close()
		require.NoError(t, <-done)
		require.Equal(t, 1, cap(q.tooLarge))
		require.Equal(t, 1, len(q.tooLarge))
	})
}
