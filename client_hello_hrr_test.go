package quic

import (
	"bytes"
	"context"
	"crypto/tls"
	"testing"
	"time"
)

// An ordinary TLS1.3 server can request a different supported key share. The
// client profile must survive HRR without changing its transport identity or
// falling back to a different TLS stack.
func TestBrowserClientHelloRetryRequest(t *testing.T) {
	serverTLS, clientTLS := browserTestTLS(t)
	serverTLS.CurvePreferences = []tls.CurveID{tls.CurveP256}
	l, err := browserListener(t, serverTLS, &Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := browserDial(t, ctx, l.Addr().String(), clientTLS, &Config{EnableDatagrams: true, ClientHelloProfile: "chromium-h3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseWithError(0, "")
	s, err := l.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.CloseWithError(0, "")
	if c.ConnectionState().ClientHelloProfile != "chromium-h3" {
		t.Fatal("profile changed during HRR")
	}
	a, err := c.ExportKeyingMaterial("HRR binding", []byte("own peer"), 32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ExportKeyingMaterial("HRR binding", []byte("own peer"), 32)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("HRR exporter mismatch: %v", err)
	}
	data := []byte("application data after real HRR")
	if err := c.SendDatagram(data); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReceiveDatagram(ctx)
	if err != nil || !bytes.Equal(data, got) {
		t.Fatalf("HRR data failed: %v", err)
	}
}
