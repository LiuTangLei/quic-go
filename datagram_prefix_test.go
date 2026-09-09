package quic

import (
	"bytes"
	"errors"
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

func prefixTestConn(limit int) *Conn {
	c := &Conn{version: protocol.Version1, peerParams: &wire.TransportParameters{MaxDatagramFrameSize: 65536}, datagramQueue: newDatagramQueue(func() {}, utils.DefaultLogger)}
	c.maxPayloadSizeEstimate.Store(uint32(limit))
	return c
}

func TestSendDatagramWithPrefixOwnershipAndLimits(t *testing.T) {
	for _, parts := range []struct{ prefix, payload []byte }{{nil, []byte("payload")}, {[]byte{0}, nil}, {[]byte{0, 2}, []byte("payload")}, {nil, nil}} {
		c := prefixTestConn(1200)
		prefix, payload := bytes.Clone(parts.prefix), bytes.Clone(parts.payload)
		want := append(bytes.Clone(prefix), payload...)
		if err := c.SendDatagramWithPrefix(prefix, payload); err != nil {
			t.Fatal(err)
		}
		clear(prefix)
		clear(payload)
		f := c.datagramQueue.Peek()
		if f == nil || !bytes.Equal(f.Data, want) {
			t.Fatal("queue retained caller-owned slices")
		}
	}
	c := prefixTestConn(1200)
	for _, sizes := range [][2]int{{1, 1200}, {1201, 0}, {600, 601}} {
		err := c.SendDatagramWithPrefix(make([]byte, sizes[0]), make([]byte, sizes[1]))
		var large *DatagramTooLargeError
		if !errors.As(err, &large) || large.MaxDatagramPayloadSize != 1200 {
			t.Fatalf("sizes %v: %v", sizes, err)
		}
		if c.datagramQueue.Peek() != nil {
			t.Fatal("oversize packet was enqueued")
		}
	}
	if err := c.SendDatagramWithPrefix([]byte{1}, make([]byte, 1199)); err != nil {
		t.Fatal(err)
	}
	c.peerParams.MaxDatagramFrameSize = 0
	if c.SendDatagramWithPrefix([]byte{1}, nil) == nil {
		t.Fatal("datagrams disabled")
	}
}

func BenchmarkHTTPDatagramPrefixCopy(b *testing.B) {
	for _, optimized := range []bool{false, true} {
		name := "previous-double-copy"
		if optimized {
			name = "single-copy"
		}
		b.Run(name, func(b *testing.B) {
			c := prefixTestConn(1400)
			payload := make([]byte, 1281)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if optimized {
					var prefix [1]byte
					err = c.SendDatagramWithPrefix(prefix[:], payload)
				} else {
					tmp := make([]byte, 0, len(payload)+8)
					tmp = append(tmp, 0)
					tmp = append(tmp, payload...)
					err = c.SendDatagram(tmp)
				}
				if err != nil {
					b.Fatal(err)
				}
				c.datagramQueue.Pop()
			}
		})
	}
}
