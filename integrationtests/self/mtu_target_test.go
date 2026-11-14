package self_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	quicproxy "github.com/quic-go/quic-go/integrationtests/tools/proxy"

	"github.com/stretchr/testify/require"
)

// plainPacketConn hides the UDP socket, as a PacketConn layered on another
// transport does: quic-go can then neither set DF nor search for the path MTU.
type plainPacketConn struct{ net.PacketConn }

// On a PacketConn without DF, the configured target is the only larger size
// tried. It is used when the path carries it and never when it does not.
func TestPacketSizeTargetWithoutDF(t *testing.T) {
	for _, test := range []struct {
		name    string
		pathMTU int
		want    int
	}{
		{name: "path carries the target", pathMTU: 1400, want: 1320},
		{name: "path does not carry the target", pathMTU: 1250, want: 1200},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := func() *quic.Config {
				return getQuicConfig(&quic.Config{InitialPacketSize: 1200, PacketSizeTarget: 1320, EnableDatagrams: true})
			}
			ln, err := quic.Listen(plainPacketConn{newUDPConnLocalhost(t)}, getTLSConfig(), config())
			require.NoError(t, err)
			defer ln.Close()

			var largest, dropped atomic.Int64
			proxy := &quicproxy.Proxy{
				Conn:        newUDPConnLocalhost(t),
				ServerAddr:  ln.Addr().(*net.UDPAddr),
				DelayPacket: func(quicproxy.Direction, net.Addr, net.Addr, []byte) time.Duration { return scaleDuration(time.Millisecond) },
				DropPacket: func(_ quicproxy.Direction, _, _ net.Addr, packet []byte) bool {
					if len(packet) > test.pathMTU {
						dropped.Add(1)
						return true
					}
					for {
						seen := largest.Load()
						if int64(len(packet)) <= seen || largest.CompareAndSwap(seen, int64(len(packet))) {
							return false
						}
					}
				},
			}
			require.NoError(t, proxy.Start())
			defer proxy.Close()

			tr := &quic.Transport{Conn: plainPacketConn{newUDPConnLocalhost(t)}}
			defer tr.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := tr.Dial(ctx, proxy.LocalAddr(), getTLSClientConfig(), config())
			require.NoError(t, err)
			defer conn.CloseWithError(0, "")
			serverConn, err := ln.Accept(ctx)
			require.NoError(t, err)
			defer serverConn.CloseWithError(0, "")
			require.Equal(t, 1200, conn.PacketSize())

			// Keep both directions sending, so that probes and their ACKs flow.
			stop := make(chan struct{})
			defer close(stop)
			for _, c := range []*quic.Conn{conn, serverConn} {
				go func() {
					for {
						select {
						case <-stop:
							return
						case <-time.After(scaleDuration(2 * time.Millisecond)):
							_ = c.SendDatagram(make([]byte, 100))
						}
					}
				}()
			}

			if test.want > 1200 {
				require.Eventually(t, func() bool {
					return conn.PacketSize() == test.want && serverConn.PacketSize() == test.want
				}, 5*time.Second, 5*time.Millisecond)
				// A datagram that needs the larger packet is now accepted and arrives.
				payload := make([]byte, 1250)
				require.NoError(t, conn.SendDatagram(payload))
				for {
					got, err := serverConn.ReceiveDatagram(ctx)
					require.NoError(t, err)
					if len(got) == len(payload) {
						break
					}
				}
				require.Zero(t, dropped.Load())
				require.LessOrEqual(t, largest.Load(), int64(test.want), "no size beyond the target was tried")
				return
			}
			// Every attempt at the target is lost; the connection stays at its
			// initial size and keeps working.
			require.Eventually(t, func() bool { return dropped.Load() >= 6 }, 5*time.Second, 5*time.Millisecond)
			require.Equal(t, 1200, conn.PacketSize())
			require.Equal(t, 1200, serverConn.PacketSize())
			var tooLarge *quic.DatagramTooLargeError
			require.ErrorAs(t, conn.SendDatagram(make([]byte, 1250)), &tooLarge)
			require.NoError(t, conn.Context().Err())
		})
	}
}
