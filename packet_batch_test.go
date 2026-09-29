package quic

import (
	"net"
	"testing"
)

type legacyBatchPacketConn struct{ net.PacketConn }

func (*legacyBatchPacketConn) WritePacketBatch([][]byte, net.Addr) error { return nil }

type sizedBatchPacketConn struct{ legacyBatchPacketConn }

func (*sizedBatchPacketConn) MaxPacketBatchSize() int { return 32 }

func TestPacketConnBatchCapacity(t *testing.T) {
	for _, tc := range []struct {
		name string
		pc   net.PacketConn
		want int
	}{
		{"no batch writer", nil, 8},
		{"legacy writer", &legacyBatchPacketConn{}, 8},
		{"sized writer", &sizedBatchPacketConn{}, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newSendQueue(&sconn{rawConn: &basicConn{PacketConn: tc.pc}}).(*sendQueue)
			if cap(q.queue) != tc.want {
				t.Fatalf("capacity=%d want=%d", cap(q.queue), tc.want)
			}
		})
	}
}
