package quic

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type portableTestSocket struct {
	net.PacketConn
	batches        atomic.Int64
	batchedPackets atomic.Int64
}

func (c *portableTestSocket) WritePacketBatch(packets [][]byte, addr net.Addr) error {
	c.batches.Add(1)
	c.batchedPackets.Add(int64(len(packets)))
	for _, p := range packets {
		if _, err := c.PacketConn.WriteTo(p, addr); err != nil {
			return err
		}
	}
	return nil
}

func TestPortableVectorRealQUICStreamIntegrity(t *testing.T) {
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
	defer udp.Close()
	pc := &portableTestSocket{PacketConn: udp}
	transport := &Transport{Conn: pc, ConnectionIDLength: 8}
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := &Config{EnableDatagrams: true, InitialPacketSize: 1400, ClientHelloProfile: "chromium-h3"}
	cfg.EnableBBRCongestionControl()
	client, err := transport.Dial(ctx, listener.Addr(), clientTLS, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(0, "")
	server, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.CloseWithError(0, "")
	payload := bytes.Repeat([]byte("portable protected packet vector integrity"), 32768)
	done := make(chan error, 1)
	go func() {
		str, err := server.AcceptStream(ctx)
		if err != nil {
			done <- err
			return
		}
		b, err := io.ReadAll(str)
		if err == nil && !bytes.Equal(b, payload) {
			err = io.ErrUnexpectedEOF
		}
		done <- err
	}()
	stream, err := client.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetWriteDeadline(time.Now().Add(8 * time.Second))
	if _, err := stream.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if client.ConnectionState().GSO {
		t.Fatal("portable writer falsely advertised kernel GSO")
	}
	if pc.batches.Load() == 0 || pc.batchedPackets.Load() <= pc.batches.Load() {
		t.Fatal("real QUIC did not use packet vectors")
	}
}
