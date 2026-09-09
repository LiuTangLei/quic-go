package quic

import (
	"bytes"
	"errors"
	"github.com/quic-go/quic-go/internal/protocol"
	"testing"
)

type batchTestConn struct {
	sendConn
	packets [][]byte
	batches int
	singles int
	failure error
}

func (c *batchTestConn) Write(b []byte, _ uint16, _ protocol.ECN) error {
	c.singles++
	c.packets = append(c.packets, bytes.Clone(b))
	return c.failure
}
func (c *batchTestConn) batchWriter() func([][]byte) error {
	return func(bufs [][]byte) error {
		c.batches++
		for _, b := range bufs {
			c.packets = append(c.packets, bytes.Clone(b))
		}
		return c.failure
	}
}

func TestSendQueueReadyBatch(t *testing.T) {
	for _, count := range []int{1, sendQueueCapacity} {
		c := &batchTestConn{}
		q := newSendQueue(c).(*sendQueue)
		for i := 0; i < count; i++ {
			b := getPacketBuffer()
			b.Data = append(b.Data[:0], byte(i), byte(i+1))
			q.Send(b, 0, protocol.ECNUnsupported)
		}
		close(q.closeCalled)
		if err := q.Run(); err != nil {
			t.Fatal(err)
		}
		if len(c.packets) != count {
			t.Fatal("packets lost")
		}
		for i, p := range c.packets {
			if !bytes.Equal(p, []byte{byte(i), byte(i + 1)}) {
				t.Fatal("order or boundary changed")
			}
		}
		if count == 1 && (c.batches != 0 || c.singles != 1) {
			t.Fatal("single packet waited for a batch")
		}
		if count > 1 && (c.batches != 1 || c.singles != 0) {
			t.Fatal("ready queue not batched")
		}
	}
}

func TestSendQueueBatchWriteError(t *testing.T) {
	want := errors.New("batch write failed")
	c := &batchTestConn{failure: want}
	q := newSendQueue(c).(*sendQueue)
	for i := 0; i < 2; i++ {
		b := getPacketBuffer()
		b.Data = append(b.Data[:0], byte(i))
		q.Send(b, 0, protocol.ECNUnsupported)
	}
	if err := q.Run(); !errors.Is(err, want) {
		t.Fatalf("write error=%v", err)
	}
	if c.batches != 1 {
		t.Fatal("unexpected retry on potentially partial send")
	}
}

func TestSendQueueBatchKeepsOOBWritesSeparate(t *testing.T) {
	c := &batchTestConn{}
	q := newSendQueue(c).(*sendQueue)
	for i := 0; i < 3; i++ {
		b := getPacketBuffer()
		b.Data = append(b.Data[:0], byte(i))
		ecn := protocol.ECNUnsupported
		if i == 1 {
			ecn = protocol.ECT0
		}
		q.Send(b, 0, ecn)
	}
	close(q.closeCalled)
	if err := q.Run(); err != nil {
		t.Fatal(err)
	}
	if c.singles != 3 || c.batches != 0 || len(c.packets) != 3 {
		t.Fatal("batch merged incompatible OOB semantics")
	}
}
