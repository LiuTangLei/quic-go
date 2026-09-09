package quic_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
)

// Hide UDP-specific read optimizations so every received datagram passes
// through the deterministic loss injector. Socket buffer requests still reach
// the real UDP socket; no global environment knobs or network rules are used.
type bbrv3LossConn struct {
	net.PacketConn
	every uint64
	reads atomic.Uint64
	drops atomic.Uint64
}

func (c *bbrv3LossConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		if c.every != 0 && c.reads.Add(1)%c.every == 0 {
			c.drops.Add(1)
			continue
		}
		return n, addr, nil
	}
}
func (c *bbrv3LossConn) SetReadBuffer(n int) error {
	return c.PacketConn.(*net.UDPConn).SetReadBuffer(n)
}
func (c *bbrv3LossConn) SetWriteBuffer(n int) error {
	return c.PacketConn.(*net.UDPConn).SetWriteBuffer(n)
}

func bbrv3TestTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}, NextProtos: []string{"bbrv3-test"}, MinVersion: tls.VersionTLS13}, &tls.Config{RootCAs: roots, ServerName: "localhost", NextProtos: []string{"bbrv3-test"}, MinVersion: tls.VersionTLS13}
}

func TestBBRv3RealQUICDuplexAndIdle(t *testing.T) {
	for _, loss := range []uint64{0, 97} {
		t.Run(fmt.Sprintf("drop-every-%d", loss), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
			defer cancel()
			serverTLS, clientTLS := bbrv3TestTLS(t)
			packet := func() *bbrv3LossConn {
				pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { pc.Close() })
				return &bbrv3LossConn{PacketConn: pc, every: loss}
			}
			spc, cpc := packet(), packet()
			cfg := &quic.Config{InitialPacketSize: 1200, DisablePathMTUDiscovery: true, MaxIdleTimeout: 30 * time.Second, HandshakeIdleTimeout: 10 * time.Second, MaxIncomingStreams: 16}
			cfg.EnableBBRv3CongestionControl()
			ln, err := quic.Listen(spc, serverTLS, cfg.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			client, err := quic.Dial(ctx, cpc, ln.Addr(), clientTLS, cfg.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseWithError(0, "test done")
			server, err := ln.Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer server.CloseWithError(0, "test done")
			for name, conn := range map[string]*quic.Conn{"client": client, "server": server} {
				if got := conn.ConnectionStats().CongestionControl; got != "bbr-v3" {
					t.Fatalf("%s controller=%s", name, got)
				}
			}
			const size = 2 << 20
			round := func(streams int) {
				t.Helper()
				errs := make(chan error, streams*2)
				go func() {
					for range streams {
						s, err := server.AcceptStream(ctx)
						if err != nil {
							errs <- err
							continue
						}
						go func() {
							s.SetDeadline(time.Now().Add(60 * time.Second))
							id := []byte{0}
							if _, err := io.ReadFull(s, id); err != nil {
								errs <- err
								return
							}
							want := bytes.Repeat([]byte{id[0]}, size)
							response := bytes.Repeat([]byte{255 - id[0]}, size)
							written := make(chan error, 1)
							go func() {
								_, err := io.Copy(s, bytes.NewReader(response))
								if err == nil {
									err = s.Close()
								}
								written <- err
							}()
							got, err := io.ReadAll(io.LimitReader(s, size+1))
							writeErr := <-written
							if err == nil {
								err = writeErr
							}
							if err == nil && sha256.Sum256(got) != sha256.Sum256(want) {
								err = fmt.Errorf("server payload mismatch: %d bytes", len(got))
							}
							errs <- err
						}()
					}
				}()
				for i := range streams {
					go func(id byte) {
						s, err := client.OpenStreamSync(ctx)
						if err != nil {
							errs <- err
							return
						}
						s.SetDeadline(time.Now().Add(60 * time.Second))
						payload := bytes.Repeat([]byte{id}, size+1)
						written := make(chan error, 1)
						go func() {
							_, err := io.Copy(s, bytes.NewReader(payload))
							if err == nil {
								err = s.Close()
							}
							written <- err
						}()
						got, err := io.ReadAll(io.LimitReader(s, size+1))
						writeErr := <-written
						if err == nil {
							err = writeErr
						}
						if err == nil && sha256.Sum256(got) != sha256.Sum256(bytes.Repeat([]byte{255 - id}, size)) {
							err = fmt.Errorf("client payload mismatch: %d bytes", len(got))
						}
						errs <- err
					}(byte(i + 1))
				}
				for range streams * 2 {
					select {
					case err := <-errs:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
			round(4)                            // 8 MiB in each direction, concurrently.
			time.Sleep(5500 * time.Millisecond) // exceed the ProbeRTT interval while idle.
			round(1)                            // 2 MiB each way after an application-limited restart.
			if loss != 0 && (spc.drops.Load() == 0 || cpc.drops.Load() == 0) {
				t.Fatal("loss injector was not exercised in both directions")
			}
			t.Logf("10 MiB each direction verified; dropped datagrams server=%d client=%d", spc.drops.Load(), cpc.drops.Load())
		})
	}
}
