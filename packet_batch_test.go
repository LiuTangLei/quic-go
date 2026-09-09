package quic

import (
	"bytes"
	"net"
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

type portableBatchRecorder struct {
	net.PacketConn
	writes  int
	batches int
	packets [][]byte
	address net.Addr
}

func (p *portableBatchRecorder) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (p *portableBatchRecorder) WriteTo(b []byte, a net.Addr) (int, error) {
	p.writes++
	p.packets = append(p.packets, bytes.Clone(b))
	p.address = a
	return len(b), nil
}
func (p *portableBatchRecorder) WritePacketBatch(bs [][]byte, a net.Addr) error {
	p.batches++
	p.address = a
	for _, b := range bs {
		p.packets = append(p.packets, bytes.Clone(b))
	}
	return nil
}

func TestPortableBatchSegmentationWithoutKernelGSO(t *testing.T) {
	recorder := &portableBatchRecorder{}
	raw := &basicConn{PacketConn: recorder}
	caps := raw.capabilities()
	if !caps.PacketBatch || caps.GSO || caps.ECN || caps.DF {
		t.Fatalf("false kernel capabilities: %+v", caps)
	}
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 23456}
	sender := newSendConn(raw, remote, packetInfo{}, utils.DefaultLogger)
	p := make([]byte, 2500)
	for i := range p {
		p[i] = byte(i)
	}
	want := bytes.Clone(p)
	if err := sender.batchWriter()([][]byte{p[:1200], p[1200:2400], p[2400:]}); err != nil {
		t.Fatal(err)
	}
	clear(p)
	if recorder.writes != 0 || recorder.batches != 1 || len(recorder.packets) != 3 {
		t.Fatal("portable batch was sent as a super-packet")
	}
	if !bytes.Equal(recorder.packets[0], want[:1200]) || !bytes.Equal(recorder.packets[1], want[1200:2400]) || !bytes.Equal(recorder.packets[2], want[2400:]) {
		t.Fatal("UDP boundaries changed")
	}
	next := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 34567}
	sender.ChangeRemoteAddr(next, packetInfo{})
	if err := sender.batchWriter()([][]byte{{1}}); err != nil {
		t.Fatal(err)
	}
	if recorder.address != next {
		t.Fatal("stale path used")
	}
	if err := sender.Write([]byte{1}, 0, protocol.ECNUnsupported); err != nil {
		t.Fatal(err)
	}
	if recorder.writes != 1 {
		t.Fatal("ordinary packet did not retain WriteTo path")
	}
}
