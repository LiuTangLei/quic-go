package quic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go/internal/wire"
	"github.com/quic-go/quic-go/testutils/simnet"
	"github.com/stretchr/testify/require"
)

// Drop one in every hundred application packets independently in each
// direction. Keeping the handshake intact makes the congestion regression
// independent of which ClientHello packetization a profile uses.
type bbrNetworkRouter struct {
	simnet.PerfectRouter
	clientPort int
	packets    [2]atomic.Uint64
	dropped    [2]atomic.Uint64
}

func (r *bbrNetworkRouter) SendPacket(p simnet.Packet) error {
	if len(p.Data) > 0 && !wire.IsLongHeaderPacket(p.Data[0]) {
		direction := 0
		if p.From.(*net.UDPAddr).Port != r.clientPort {
			direction = 1
		}
		if r.packets[direction].Add(1)%100 == 0 {
			r.dropped[direction].Add(1)
			return nil
		}
	}
	return r.PerfectRouter.SendPacket(p)
}

// Each endpoint has its own serial bottleneck. A packet waits behind bytes
// already queued in that direction, then incurs the fixed propagation delay.
func bbrNetworkLatency(oneWay time.Duration, bytesPerSecond int64) func(simnet.Packet) time.Duration {
	var mu sync.Mutex
	var finish time.Time
	return func(p simnet.Packet) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if finish.Before(now) {
			finish = now
		}
		finish = finish.Add(time.Duration(int64(len(p.Data)) * int64(time.Second) / bytesPerSecond))
		return finish.Sub(now) + oneWay
	}
}

func TestBBRSerializedNetworkLossAndIdleRestart(t *testing.T) {
	for _, profile := range []string{"", "chromium-h3"} {
		name := profile
		if name == "" {
			name = "standard"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const rtt = 60 * time.Millisecond
				const bytesPerSecond = 500_000 // 4 Mbps, per direction
				clientAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9101}
				serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9102}
				router := &bbrNetworkRouter{clientPort: clientAddr.Port}
				network := &simnet.Simnet{Router: router}
				clientSocket := network.NewEndpoint(clientAddr, simnet.NodeBiDiLinkSettings{LatencyFunc: bbrNetworkLatency(rtt/2, bytesPerSecond)})
				defer clientSocket.Close()
				serverSocket := network.NewEndpoint(serverAddr, simnet.NodeBiDiLinkSettings{LatencyFunc: bbrNetworkLatency(rtt/2, bytesPerSecond)})
				defer serverSocket.Close()
				require.NoError(t, network.Start())
				defer network.Close()

				serverTLS, clientTLS := browserTestTLS(t)
				config := &Config{
					EnableBBR:                      true,
					MaxIdleTimeout:                 time.Minute,
					HandshakeIdleTimeout:           3 * time.Second,
					InitialStreamReceiveWindow:     1 << 20,
					MaxStreamReceiveWindow:         1 << 20,
					InitialConnectionReceiveWindow: 2 << 20,
					MaxConnectionReceiveWindow:     2 << 20,
				}
				listener, err := Listen(serverSocket, serverTLS, config)
				require.NoError(t, err)
				defer listener.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				clientConfig := config.Clone()
				clientConfig.ClientHelloProfile = profile
				client, err := Dial(ctx, clientSocket, serverAddr, clientTLS, clientConfig)
				require.NoError(t, err)
				defer client.CloseWithError(0, "")
				server, err := listener.Accept(ctx)
				require.NoError(t, err)
				defer server.CloseWithError(0, "")
				require.True(t, client.config.EnableBBR)
				require.True(t, server.config.EnableBBR)
				require.Equal(t, profile, client.ConnectionState().ClientHelloProfile)

				pattern := make([]byte, 256)
				for i := range pattern {
					pattern[i] = byte(i)
				}
				bulk := bytes.Repeat(pattern, (8<<20)/len(pattern))
				for _, direction := range []struct {
					name             string
					sender, receiver *Conn
				}{{"forward", client, server}, {"reverse", server, client}} {
					elapsed := bbrNetworkTransfer(t, direction.sender, direction.receiver, bulk, bytesPerSecond)
					// Serialization alone requires over 16 seconds. Keep the
					// flow active beyond min-RTT expiry; unit tests separately
					// assert the precise ProbeRTT transitions.
					require.Greater(t, elapsed, 10*time.Second)
					t.Logf("%s: %d bytes in %s virtual time, %.2f Mbps", direction.name, len(bulk), elapsed, float64(len(bulk))*8/elapsed.Seconds()/1e6)
				}

				time.Sleep(12 * time.Second)
				resumed := bulk[:512<<10]
				elapsed := bbrNetworkTransfer(t, server, client, resumed, bytesPerSecond)
				t.Logf("reverse after 12s idle: %d bytes in %s virtual time, %.2f Mbps", len(resumed), elapsed, float64(len(resumed))*8/elapsed.Seconds()/1e6)
				for i, conn := range []*Conn{client, server} {
					require.Positive(t, router.dropped[i].Load(), "loss fixture must affect both send directions")
					require.Positive(t, conn.ConnectionStats().PacketsLost, "sender must detect the injected loss")
				}
			})
		})
	}
}

func bbrNetworkTransfer(t *testing.T, sender, receiver *Conn, payload []byte, bytesPerSecond int64) time.Duration {
	t.Helper()
	// Permit retransmission, pacing, and startup overhead, while still requiring
	// roughly a quarter of the simulated link's capacity. This is a completion
	// regression bound, not a comparison with other congestion controllers.
	budget := 4*time.Duration(int64(len(payload))*int64(time.Second)/bytesPerSecond) + 2*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	started := time.Now()
	writeDone := make(chan error, 1)
	go func() {
		stream, err := sender.OpenUniStreamSync(ctx)
		if err != nil {
			writeDone <- err
			return
		}
		_ = stream.SetWriteDeadline(started.Add(budget))
		n, err := stream.Write(payload)
		if err == nil && n != len(payload) {
			err = fmt.Errorf("short stream write: %d of %d bytes", n, len(payload))
		}
		if err == nil {
			err = stream.Close()
		}
		writeDone <- err
	}()
	stream, err := receiver.AcceptUniStream(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.SetReadDeadline(started.Add(budget)))
	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Len(t, got, len(payload))
	require.True(t, bytes.Equal(payload, got), "recovered stream data must remain exact")
	select {
	case err := <-writeDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("sender did not finish before the transfer deadline")
	}
	elapsed := time.Since(started)
	require.Less(t, elapsed, budget)
	return elapsed
}
