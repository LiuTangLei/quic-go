package self_test

import (
	"crypto/tls"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/testutils/simnet"
	"github.com/stretchr/testify/require"
)

// Supplement, not replace, the original stochastic packet-loss matrix. Use a
// separate seeded sequence per direction so failures have a replayable loss
// pattern rather than consuming the process-wide random generator.
func TestBBRv3DefaultSeededHandshakeLoss(t *testing.T) {
	for _, seed := range []uint64{1, 7, 42, 20260923} {
		for _, retry := range []bool{false, true} {
			for _, serverFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("seed=%d/retry=%t/server-first=%t", seed, retry, serverFirst), func(t *testing.T) {
					t.Logf("one-third packet-drop sequences, seed=%d, retry=%t, server-first=%t", seed, retry, serverFirst)
					synctest.Test(t, func(t *testing.T) {
						clientAddr := &net.UDPAddr{IP: net.IPv4(1, 0, 0, 1), Port: 9001}
						serverAddr := &net.UDPAddr{IP: net.IPv4(1, 0, 0, 2), Port: 9002}
						rng := [2]*mrand.Rand{
							mrand.New(mrand.NewPCG(seed, seed+101)),
							mrand.New(mrand.NewPCG(seed+211, seed+307)),
						}
						var mu sync.Mutex
						var consecutive, dropped [2]int
						n := &simnet.Simnet{Router: &directionAwareDroppingRouter{
							ClientAddr: clientAddr, ServerAddr: serverAddr,
							Drop: func(dir direction, _ simnet.Packet) bool {
								mu.Lock()
								defer mu.Unlock()
								i := 0
								if dir == directionToServer {
									i = 1
								}
								drop := rng[i].IntN(3) == 0 && consecutive[i] < 10
								if drop {
									consecutive[i]++
									dropped[i]++
								} else {
									consecutive[i] = 0
								}
								return drop
							},
						}}
						settings := simnet.NodeBiDiLinkSettings{Latency: 10 * time.Millisecond}
						clientSocket := n.NewEndpoint(clientAddr, settings)
						defer clientSocket.Close()
						serverSocket := n.NewEndpoint(serverAddr, settings)
						defer serverSocket.Close()
						require.NoError(t, n.Start())
						defer n.Close()
						tr := &quic.Transport{Conn: serverSocket, VerifySourceAddress: func(net.Addr) bool { return retry }}
						defer tr.Close()
						const timeout = 2 * time.Minute
						ln, err := tr.Listen(getTLSConfig(), getQuicConfig(&quic.Config{
							MaxIdleTimeout: timeout, HandshakeIdleTimeout: timeout, DisablePathMTUDiscovery: true,
						}))
						require.NoError(t, err)
						defer ln.Close()
						clientTLS := getTLSClientConfig()
						clientTLS.CurvePreferences = []tls.CurveID{tls.CurveP384}
						fn := dropTestProtocolClientSpeaksFirst
						if serverFirst {
							fn = dropTestProtocolServerSpeaksFirst
						}
						conn := fn(t, ln, clientSocket, clientTLS, timeout, GeneratePRData(5000))
						require.Equal(t, "bbr-v3", conn.ConnectionStats().CongestionControl)
						mu.Lock()
						counts := dropped
						mu.Unlock()
						t.Logf("seed=%d dropped-to-client=%d dropped-to-server=%d", seed, counts[0], counts[1])
					})
				})
			}
		}
	}
}
