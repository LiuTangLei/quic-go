package quic

import (
	"bytes"
	"github.com/quic-go/quic-go/internal/protocol"
	"testing"
)

func TestSendQueueVariableLengthVector(t *testing.T) {
	c := &batchTestConn{}
	q := newSendQueue(c).(*sendQueue)
	sizes := []int{1, 13, 1279, 333, 1280, 72, 1281, 99}
	buffers := make([]*packetBuffer, len(sizes))
	for i, n := range sizes {
		b := getPacketBuffer()
		b.Data = append(b.Data[:0], bytes.Repeat([]byte{byte(i + 1)}, n)...)
		buffers[i] = b
	}
	q.SendBatch(buffers, protocol.ECNUnsupported)
	clear(buffers) // the queue owns the pointer vector as well as its buffers
	close(q.closeCalled)
	if err := q.Run(); err != nil {
		t.Fatal(err)
	}
	if c.batches != 1 || c.singles != 0 || len(c.packets) != len(sizes) {
		t.Fatal("variable vector not batched")
	}
	for i, n := range sizes {
		if len(c.packets[i]) != n || !bytes.Equal(c.packets[i], bytes.Repeat([]byte{byte(i + 1)}, n)) {
			t.Fatal("packet padding, contents or boundaries changed")
		}
	}
}

func TestSendQueueVectorAndOrdinaryPacketOrder(t *testing.T) {
	c := &batchTestConn{}
	q := newSendQueue(c).(*sendQueue)
	var batch []*packetBuffer
	for i := 0; i < 3; i++ {
		b := getPacketBuffer()
		b.Data = append(b.Data[:0], byte(i))
		batch = append(batch, b)
	}
	q.SendBatch(batch, protocol.ECNUnsupported)
	b := getPacketBuffer()
	b.Data = append(b.Data[:0], 3)
	q.Send(b, 0, protocol.ECNUnsupported)
	close(q.closeCalled)
	if err := q.Run(); err != nil {
		t.Fatal(err)
	}
	for i, p := range c.packets {
		if !bytes.Equal(p, []byte{byte(i)}) {
			t.Fatal("vector/ordinary order changed")
		}
	}
	if len(c.packets) != 4 {
		t.Fatal("packet lost")
	}
}
