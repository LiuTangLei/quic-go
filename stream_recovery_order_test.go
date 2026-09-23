package quic

import (
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRepeatedLossRepairsOldestStreamHoleFirst(t *testing.T) {
	sender := NewMockStreamSender(gomock.NewController(t))
	s := newSendStream(t.Context(), 0, sender, nil, false)
	sender.EXPECT().onHasStreamRetransmission(protocol.StreamID(0), s).AnyTimes()
	h := (*sendStreamAckHandler)(s)
	// Newer first losses arrive before the older retransmission is declared
	// lost again. Packet-number FIFO would keep starving offset zero.
	for _, off := range []protocol.ByteCount{1000, 2000, 3000, 0, 500} {
		s.numOutstandingFrames++
		h.OnLost(&wire.StreamFrame{Offset: off, Data: make([]byte, 100), DataLenPresent: true})
	}
	for i, off := range []protocol.ByteCount{0, 500, 1000, 2000, 3000} {
		f, more := s.popRetransmissionFrame(protocol.MaxByteCount, protocol.Version1)
		require.NotNil(t, f.Frame)
		require.Equal(t, off, f.Frame.Offset)
		require.Equal(t, i < 4, more)
		f.Handler.OnAcked(f.Frame)
	}
	require.Empty(t, s.retransmissionQueue)
	require.Zero(t, s.numOutstandingFrames)
}
