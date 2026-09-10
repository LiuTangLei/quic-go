package quic

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

func TestDatagramBatchOwnershipBoundsAndWake(t *testing.T) {
	c := prefixTestConn(1400)
	wakes := 0
	c.datagramQueue = newDatagramQueue(func() { wakes++ }, utils.DefaultLogger)
	prefix := []byte{0, 2}
	packets := make([][]byte, 32)
	for i := range packets {
		packets[i] = bytes.Repeat([]byte{byte(i + 1)}, 1200+i)
	}
	n, err := c.SendDatagramsWithPrefix(prefix, packets)
	if err != nil || n != 32 || wakes != 1 {
		t.Fatalf("n=%d err=%v wakes=%d", n, err, wakes)
	}
	clear(prefix)
	for _, p := range packets {
		clear(p)
	}
	for i := range packets {
		f := c.datagramQueue.Peek()
		want := append([]byte{0, 2}, bytes.Repeat([]byte{byte(i + 1)}, 1200+i)...)
		if f == nil || !bytes.Equal(f.Data, want) {
			t.Fatal("caller ownership or boundaries lost")
		}
		c.datagramQueue.Pop()
	}
	n, err = c.SendDatagramsWithPrefix([]byte{1}, [][]byte{{1}, make([]byte, 1400)})
	var sizeErr *DatagramTooLargeError
	if n != 0 || !errors.As(err, &sizeErr) || c.datagramQueue.Peek() != nil {
		t.Fatal("oversize validation partially sent")
	}
	if n, err = c.SendDatagramsWithPrefix(nil, make([][]byte, 129)); n != 0 || err == nil {
		t.Fatal("unbounded batch accepted")
	}
}
func TestDatagramBatchPartialCountAndClose(t *testing.T) {
	c := prefixTestConn(1400)
	for range 30 {
		if err := c.datagramQueue.Add(&wire.DatagramFrame{Data: []byte{0}}); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, e := c.SendDatagramsWithPrefix([]byte{0}, [][]byte{{1}, {2}, {3}, {4}})
		done <- result{n, e}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.datagramQueue.sendMx.Lock()
		size := c.datagramQueue.sendQueue.Len()
		c.datagramQueue.sendMx.Unlock()
		if size == 32 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("batch did not fill bounded queue")
		}
		time.Sleep(time.Millisecond)
	}
	failure := errors.New("closed for test")
	c.datagramQueue.CloseWithError(failure)
	select {
	case r := <-done:
		if r.n != 2 || !errors.Is(r.err, failure) {
			t.Fatalf("partial=%+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked batch did not cancel")
	}
	if n, e := c.SendDatagramsWithPrefix(nil, [][]byte{{1}}); n != 0 || !errors.Is(e, failure) {
		t.Fatal("accepted batch after close")
	}
}
