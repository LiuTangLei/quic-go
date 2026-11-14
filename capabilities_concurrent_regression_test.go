//go:build linux

package quic

import (
	"net"
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSendConnConcurrentCapabilitiesDuringGSOFailure(t *testing.T) {
	raw := NewMockRawConn(gomock.NewController(t))
	raw.EXPECT().LocalAddr().Return(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	raw.EXPECT().capabilities().Return(connCapabilities{GSO: true}).AnyTimes()
	raw.EXPECT().WritePacket(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(p []byte, _ net.Addr, _ []byte, gso uint16, _ protocol.ECN) (int, error) {
			if gso > 0 {
				return 0, errGSO
			}
			return len(p), nil
		}).AnyTimes()
	c := newSendConn(raw, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4321}, packetInfo{}, utils.DefaultLogger)
	ready, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(ready)
		for range 100000 {
			_ = c.capabilities()
		}
		close(done)
	}()
	<-ready
	require.NoError(t, c.Write([]byte("foobar"), 4, protocol.ECNCE))
	<-done
	require.False(t, c.capabilities().GSO)
}
