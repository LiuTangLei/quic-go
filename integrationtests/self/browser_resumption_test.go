package self_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

type notifyingSessionCache struct {
	tls.ClientSessionCache
	ready chan struct{}
	once  sync.Once
}

func (c *notifyingSessionCache) Put(k string, s *tls.ClientSessionState) {
	c.ClientSessionCache.Put(k, s)
	if s != nil {
		c.once.Do(func() { close(c.ready) })
	}
}

func TestBrowserProfileDeferredTicketAndZeroRTT(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(7), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h3"}}
	listener, err := quic.ListenAddrEarly("127.0.0.1:0", serverTLS, getQuicConfig(&quic.Config{Allow0RTT: true, DeferSessionTicket: true}))
	require.NoError(t, err)
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		data  string
		early bool
		err   error
	}
	received := make(chan result, 3)
	go func() {
		for range 3 {
			q, err := listener.Accept(ctx)
			if err != nil {
				received <- result{err: err}
				return
			}
			go func() {
				stream, err := q.AcceptUniStream(ctx)
				if err != nil {
					received <- result{err: err}
					return
				}
				data, err := io.ReadAll(stream)
				early := q.ConnectionState().Used0RTT
				select {
				case <-q.HandshakeComplete():
				case <-ctx.Done():
					received <- result{err: ctx.Err()}
					return
				}
				if err == nil {
					err = q.SendSessionTicket()
				}
				received <- result{string(data), early, err}
			}()
		}
	}()
	cache := &notifyingSessionCache{ClientSessionCache: tls.NewLRUClientSessionCache(4), ready: make(chan struct{})}
	clientTLS := &tls.Config{RootCAs: roots, ServerName: "localhost", NextProtos: []string{"h3"}, ClientSessionCache: cache}
	cfg := getQuicConfig(&quic.Config{ClientHelloProfile: "chromium-h3"})
	first, err := quic.DialAddr(ctx, listener.Addr().String(), clientTLS, cfg)
	require.NoError(t, err)
	defer first.CloseWithError(0, "")
	_, premature := cache.Get("localhost")
	require.False(t, premature, "TLS alone must not issue a deferred application ticket")
	send := func(q *quic.Conn, s string) {
		stream, err := q.OpenUniStreamSync(ctx)
		require.NoError(t, err)
		_, err = stream.Write([]byte(s))
		require.NoError(t, err)
		require.NoError(t, stream.Close())
	}
	send(first, "full")
	got := <-received
	require.NoError(t, got.err)
	require.Equal(t, "full", got.data)
	require.False(t, got.early)
	select {
	case <-cache.ready:
	case <-ctx.Done():
		t.Fatal("deferred ticket was not stored")
	}
	second, err := quic.DialAddrEarly(ctx, listener.Addr().String(), clientTLS, cfg)
	require.NoError(t, err)
	defer second.CloseWithError(0, "")
	send(second, "early")
	got = <-received
	require.NoError(t, got.err)
	require.Equal(t, "early", got.data)
	require.True(t, got.early, "profiled connection must send real QUIC 0-RTT")
	third, err := quic.DialAddr(ctx, listener.Addr().String(), clientTLS, cfg)
	require.NoError(t, err)
	defer third.CloseWithError(0, "")
	send(third, "resumed without early data")
	got = <-received
	require.NoError(t, got.err)
	require.False(t, got.early, "ordinary Dial must disable early data even with a cached browser session")
	require.True(t, third.ConnectionState().TLS.DidResume)
	require.Equal(t, "resumed without early data", got.data)
	rejectedPin := errors.New("current pin was revoked")
	verifiedResume := false
	clientTLS = clientTLS.Clone()
	clientTLS.VerifyConnection = func(s tls.ConnectionState) error { verifiedResume = s.DidResume; return rejectedPin }
	failed, err := quic.DialAddr(ctx, listener.Addr().String(), clientTLS, cfg)
	if failed != nil {
		failed.CloseWithError(0, "")
	}
	require.ErrorIs(t, err, rejectedPin)
	require.True(t, verifiedResume, "current verification policy must run on resumed TLS")
}
