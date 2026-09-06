package quic

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func browserTestTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "owned-h3.test"}, DNSNames: []string{"owned-h3.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	server := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: k, Leaf: leaf}}}
	client := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, RootCAs: roots, ServerName: "owned-h3.test"}
	return server, client
}

func TestBrowserQUICHandshakeExporterAndDatagrams(t *testing.T) {
	for _, profile := range []string{"", "chromium-h3"} {
		t.Run(profile, func(t *testing.T) {
			serverTLS, clientTLS := browserTestTLS(t)
			var verified atomic.Int32
			clientTLS.VerifyConnection = func(s tls.ConnectionState) error {
				verified.Add(1)
				if len(s.VerifiedChains) == 0 || s.Version != tls.VersionTLS13 || s.NegotiatedProtocol != "h3" {
					return errors.New("TLS identity/ALPN missing")
				}
				return nil
			}
			cfg := &Config{EnableDatagrams: true, HandshakeIdleTimeout: 2 * time.Second}
			cfg.EnableBBRCongestionControl()
			listener, err := browserListener(t, serverTLS, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			clientConfig := cfg.Clone()
			clientConfig.ClientHelloProfile = profile
			client, err := browserDial(t, ctx, listener.Addr().String(), clientTLS, clientConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseWithError(0, "")
			server, err := listener.Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer server.CloseWithError(0, "")
			if verified.Load() != 1 {
				t.Fatalf("verification calls=%d", verified.Load())
			}
			if client.ConnectionState().ClientHelloProfile != profile || server.ConnectionState().ClientHelloProfile != "" {
				t.Fatal("wrong actual role/profile")
			}
			a, err := client.ExportKeyingMaterial("own CONNECT authorization", []byte("request-binding"), 32)
			if err != nil {
				t.Fatal(err)
			}
			b, err := server.ExportKeyingMaterial("own CONNECT authorization", []byte("request-binding"), 32)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) {
				t.Fatal("TLS exporter differs across real handshake")
			}
			b2, err := server.ExportKeyingMaterial("own CONNECT authorization", []byte("different-request"), 32)
			if err != nil || bytes.Equal(a, b2) {
				t.Fatal("context binding missing")
			}
			for i, pair := range [][2]*Conn{{client, server}, {server, client}} {
				want := bytes.Repeat([]byte{byte(20 + i)}, 1024)
				if err := pair[0].SendDatagram(want); err != nil {
					t.Fatal(err)
				}
				got, err := pair[1].ReceiveDatagram(ctx)
				if err != nil || !bytes.Equal(want, got) {
					t.Fatal("datagram mismatch", err)
				}
			}
		})
	}
}

func TestBrowserQUICVerificationCannotBeBypassed(t *testing.T) {
	serverTLS, clientTLS := browserTestTLS(t)
	l, err := browserListener(t, serverTLS, &Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, kind := range []string{"wrong-host", "wrong-pin", "unknown-profile", "wrong-alpn", "session-cache", "ech"} {
		t.Run(kind, func(t *testing.T) {
			cfg := clientTLS.Clone()
			qc := &Config{ClientHelloProfile: "chromium-h3", HandshakeIdleTimeout: time.Second}
			switch kind {
			case "wrong-host":
				cfg.ServerName = "not-owned.test"
			case "wrong-pin":
				cfg.InsecureSkipVerify = true
				cfg.VerifyConnection = func(tls.ConnectionState) error { return errors.New("pin rejected") }
			case "unknown-profile":
				qc.ClientHelloProfile = "chrome-invented"
			case "wrong-alpn":
				cfg.NextProtos = []string{"not-h3"}
			case "session-cache":
				cfg.ClientSessionCache = tls.NewLRUClientSessionCache(2)
			case "ech":
				cfg.EncryptedClientHelloConfigList = []byte{1, 2, 3}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := browserDial(t, ctx, l.Addr().String(), cfg, qc)
			if err == nil {
				c.CloseWithError(0, "")
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}

func TestBrowserAndStandardConcurrentSharedTransport(t *testing.T) {
	serverTLS, clientTLS := browserTestTLS(t)
	l, err := browserListener(t, serverTLS, &Config{EnableDatagrams: true, HandshakeIdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	tr := &Transport{Conn: udp, ConnectionIDLength: 8}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const n = 8
	failures := make(chan error, n*2)
	var serverWG sync.WaitGroup
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		for range n {
			c, err := l.Accept(ctx)
			if err != nil {
				failures <- err
				return
			}
			serverWG.Add(1)
			go func() {
				defer serverWG.Done()
				msg, err := c.ReceiveDatagram(ctx)
				if err == nil {
					err = c.SendDatagram(msg)
				}
				if err != nil {
					failures <- err
				}
			}()
		}
	}()
	var clients sync.WaitGroup
	exporters := make(chan [32]byte, n)
	for i := range n {
		clients.Add(1)
		go func(i int) {
			defer clients.Done()
			profile := ""
			if i%2 == 0 {
				profile = "chromium-h3"
			}
			qc := &Config{EnableDatagrams: true, ClientHelloProfile: profile, HandshakeIdleTimeout: 3 * time.Second}
			qc.EnableBBRCongestionControl()
			c, err := tr.Dial(ctx, l.Addr(), clientTLS.Clone(), qc)
			if err != nil {
				failures <- err
				return
			}
			defer c.CloseWithError(0, "")
			if c.ConnectionState().ClientHelloProfile != profile {
				failures <- errors.New("profile crossed connections")
				return
			}
			ekm, err := c.ExportKeyingMaterial("isolation", nil, 32)
			if err != nil {
				failures <- err
				return
			}
			exporters <- sha256.Sum256(ekm)
			want := bytes.Repeat([]byte{byte(i + 1)}, 400)
			if err := c.SendDatagram(want); err != nil {
				failures <- err
				return
			}
			got, err := c.ReceiveDatagram(ctx)
			if err != nil {
				failures <- err
				return
			}
			if !bytes.Equal(want, got) {
				failures <- errors.New("payload crossed shared CID connections")
			}
		}(i)
	}
	clients.Wait()
	serverWG.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	close(exporters)
	seen := map[[32]byte]bool{}
	for h := range exporters {
		if seen[h] {
			t.Error("TLS exporter reused across sessions")
		}
		seen[h] = true
	}
	if len(seen) != n {
		t.Errorf("handshakes=%d", len(seen))
	}
}
