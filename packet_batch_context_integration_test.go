package quic

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type contextPortableSocket struct {
	net.PacketConn
	blocked atomic.Bool
	entered chan struct{}
}

func (c *contextPortableSocket) WritePacketBatch(p [][]byte, a net.Addr) error {
	for _, b := range p {
		if _, err := c.WriteTo(b, a); err != nil {
			return err
		}
	}
	return nil
}
func (c *contextPortableSocket) WritePacketBatchContext(ctx context.Context, p [][]byte, a net.Addr) error {
	if c.blocked.Load() {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return c.WritePacketBatch(p, a)
}
func TestPortableBlockedWriteCloseKeepsSharedSocket(t *testing.T) {
	serverTLS, clientTLS := browserTestTLS(t)
	listener, err := browserListener(t, serverTLS, &Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pc := &contextPortableSocket{PacketConn: udp, entered: make(chan struct{}, 1)}
	tr := &Transport{Conn: pc, ConnectionIDLength: 8}
	defer tr.Close()
	defer udp.Close()
	defer pc.blocked.Store(false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg := &Config{EnableDatagrams: true}
	cfg.EnableBBRCongestionControl()
	first, err := tr.Dial(ctx, listener.Addr(), clientTLS, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.CloseWithError(0, "")
	pc.blocked.Store(true)
	if err = first.SendDatagram([]byte("block queue")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pc.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	done := make(chan struct{})
	go func() { first.CloseWithError(0, ""); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection close deadlocked on shared writer")
	}
	pc.blocked.Store(false)
	second, err := tr.Dial(ctx, listener.Addr(), clientTLS, cfg)
	if err != nil {
		t.Fatalf("shared socket unusable: %v", err)
	}
	defer second.CloseWithError(0, "")
	peer, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.CloseWithError(0, "")
	payload := []byte("another authenticated connection")
	if err = second.SendDatagram(payload); err != nil {
		t.Fatal(err)
	}
	received, err := peer.ReceiveDatagram(ctx)
	if err != nil || string(received) != string(payload) {
		t.Fatalf("socket reuse: %v", err)
	}
}
