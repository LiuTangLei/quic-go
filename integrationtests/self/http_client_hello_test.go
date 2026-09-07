package self_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

// The default HTTP/3 transport uses DialEarly even without a session cache.
// A fresh browser-profile connection must work through that public entry point
// and must wait for an authenticated handshake before sending a request.
func TestHTTPBrowserClientHelloProfile(t *testing.T) {
	// The Chromium-style signature list supports ECDSA / RSA; the generic
	// integration fixture uses Ed25519, which this profile does not advertise.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(42), DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	tlsConf := &tls.Config{RootCAs: roots}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.DidResume {
			http.Error(w, "request arrived without a fresh completed handshake", http.StatusBadRequest)
			return
		}
		io.WriteString(w, "hello over profiled HTTP/3")
	})
	port := startHTTPServer(t, mux, func(s *http3.Server) { s.TLSConfig = serverTLS })
	var verified atomic.Int32
	tlsConf.VerifyConnection = func(s tls.ConnectionState) error {
		verified.Add(1)
		if s.NegotiatedProtocol != http3.NextProtoH3 || len(s.VerifiedChains) == 0 {
			return fmt.Errorf("missing verified HTTP/3 identity")
		}
		return nil
	}
	tr := &http3.Transport{
		TLSClientConfig: tlsConf,
		QUICConfig: getQuicConfig(&quic.Config{
			ClientHelloProfile:   "chromium-h3",
			HandshakeIdleTimeout: 2 * time.Second,
		}),
	}
	t.Cleanup(func() { tr.Close() })
	client := &http.Client{Transport: tr}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Start with an explicitly early-data-capable method on an empty cache.
	// Subsequent ordinary requests should reuse the same authenticated session.
	for _, method := range []string{http3.MethodGet0RTT, http.MethodGet, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("https://localhost:%d/hello", port), nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "hello over profiled HTTP/3", string(body))
		require.Equal(t, 3, resp.ProtoMajor)
		require.True(t, resp.TLS.HandshakeComplete)
		require.False(t, resp.TLS.DidResume)
	}
	require.EqualValues(t, 1, verified.Load(), "requests should reuse a verified connection")
}
