package self_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	quicproxy "github.com/quic-go/quic-go/integrationtests/tools/proxy"
	"github.com/stretchr/testify/require"
)

// TestConnectionMigrationThenMTUShrinkWithoutDF exercises the PacketConn used by the
// application: no DF/GSO, a validated 1320-byte target, an actual address
// migration, and then a silent path shrink on the same QUIC connection.
func TestConnectionMigrationThenMTUShrinkWithoutDF(t *testing.T) {
	config := func() *quic.Config {
		return getQuicConfig(&quic.Config{InitialPacketSize: 1200, PacketSizeTarget: 1320, EnableDatagrams: true})
	}
	listener, err := quic.Listen(plainPacketConn{newUDPConnLocalhost(t)}, getTLSConfig(), config())
	require.NoError(t, err)
	defer listener.Close()
	tr1 := &quic.Transport{Conn: plainPacketConn{newUDPConnLocalhost(t)}}
	defer tr1.Close()
	tr2 := &quic.Transport{Conn: plainPacketConn{newUDPConnLocalhost(t)}}
	defer tr2.Close()

	var limit, dropped, path2Packets atomic.Int64
	limit.Store(1400)
	proxy := &quicproxy.Proxy{
		Conn: newUDPConnLocalhost(t), ServerAddr: listener.Addr().(*net.UDPAddr),
		DelayPacket: func(_ quicproxy.Direction, _, _ net.Addr, _ []byte) time.Duration { return time.Millisecond },
		DropPacket: func(direction quicproxy.Direction, from, _ net.Addr, packet []byte) bool {
			if direction == quicproxy.DirectionIncoming && from.String() == tr2.Conn.LocalAddr().String() {
				path2Packets.Add(1)
			}
			if int64(len(packet)) > limit.Load() {
				dropped.Add(1)
				return true
			}
			return false
		},
	}
	require.NoError(t, proxy.Start())
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := tr1.Dial(ctx, proxy.LocalAddr(), getTLSClientConfig(), config())
	require.NoError(t, err)
	defer client.CloseWithError(0, "")
	server, err := listener.Accept(ctx)
	require.NoError(t, err)
	defer server.CloseWithError(0, "")

	stop := make(chan struct{})
	var workers sync.WaitGroup
	// Public getters are used by net.Conn adapters and telemetry while the
	// connection loop switches send connections. Cover both getters throughout.
	workers.Add(1)
	go func() {
		defer workers.Done()
		tick := time.NewTicker(100 * time.Microsecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = client.LocalAddr().String()
				_ = client.RemoteAddr().String()
				_ = server.LocalAddr().String()
				_ = server.RemoteAddr().String()
			}
		}
	}()
	for _, conn := range []*quic.Conn{client, server} {
		workers.Add(1)
		go func(conn *quic.Conn) {
			defer workers.Done()
			tick := time.NewTicker(2 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					// Full-size data loss, unlike small keepalives, exercises
					// urgent target revalidation after the silent shrink.
					payload := make([]byte, 100)
					if conn.PacketSize() > 1200 {
						payload = make([]byte, 1250)
					}
					if err := conn.SendDatagram(payload); err != nil && conn.Context().Err() != nil {
						return
					}
				}
			}
		}(conn)
	}
	defer func() {
		close(stop)
		// A failed assertion must also unblock a sender waiting on queue space.
		client.CloseWithError(0, "test cleanup")
		server.CloseWithError(0, "test cleanup")
		workers.Wait()
	}()
	large := func() bool { return client.PacketSize() == 1320 && server.PacketSize() == 1320 }
	require.Eventually(t, large, 5*time.Second, 5*time.Millisecond)
	require.Zero(t, dropped.Load())

	oldServerRemote := server.RemoteAddr().String()
	path, err := client.AddPath(tr2)
	require.NoError(t, err)
	require.NoError(t, path.Probe(ctx))
	require.NoError(t, path.Switch())
	require.Eventually(t, func() bool {
		return path2Packets.Load() > 0 && server.RemoteAddr().String() != oldServerRemote &&
			client.LocalAddr().String() == tr2.Conn.LocalAddr().String() && large()
	}, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, "bbr-v3", client.ConnectionStats().CongestionControl)
	require.Equal(t, "bbr-v3", server.ConnectionStats().CongestionControl)

	started := time.Now()
	limit.Store(1200)
	require.Eventually(t, func() bool { return client.PacketSize() == 1200 && server.PacketSize() == 1200 },
		5*time.Second, 5*time.Millisecond)
	t.Logf("same-connection fallback after migration: %s, oversized packets dropped: %d", time.Since(started), dropped.Load())
	require.Positive(t, dropped.Load())
	require.NoError(t, client.Context().Err())
	require.NoError(t, server.Context().Err())
	require.Equal(t, "bbr-v3", client.ConnectionStats().CongestionControl)
	require.Equal(t, "bbr-v3", server.ConnectionStats().CongestionControl)

	// Both reliable-stream directions must still deliver the last byte and FIN.
	payload := bytes.Repeat([]byte("migration-then-shrink\x00"), 8000)
	for i, sender := range []*quic.Conn{client, server} {
		receiver := []*quic.Conn{server, client}[i]
		received := make(chan []byte, 1)
		receiveErr := make(chan error, 1)
		go func() {
			stream, err := receiver.AcceptStream(ctx)
			if err != nil {
				receiveErr <- err
				return
			}
			data, err := io.ReadAll(stream)
			if err != nil {
				receiveErr <- err
				return
			}
			received <- data
		}()
		stream, err := sender.OpenStream()
		require.NoError(t, err)
		require.NoError(t, stream.SetWriteDeadline(time.Now().Add(5*time.Second)))
		_, err = stream.Write(payload)
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		select {
		case data := <-received:
			require.Equal(t, payload, data)
		case err := <-receiveErr:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		require.NoError(t, stream.WaitWriteAcknowledged(ctx))
	}
}
