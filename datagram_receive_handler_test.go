package quic

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/wire"
)

func receiveHandlerTestConn() *Conn {
	c := prefixTestConn(1400)
	c.config = &Config{EnableDatagrams: true}
	return c
}

func TestDatagramReceiveHandlerDispatchAndOwnership(t *testing.T) {
	c := receiveHandlerTestConn()
	var taken []byte
	if err := c.SetDatagramReceiveHandler(func(b []byte) bool {
		if len(b) == 0 || b[0] != 7 {
			return false
		}
		taken = bytes.Clone(b)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	selected := []byte{7, 2, 3}
	if err := c.handleDatagramFrame(&wire.DatagramFrame{Data: selected}); err != nil {
		t.Fatal(err)
	}
	clear(selected)
	if !bytes.Equal(taken, []byte{7, 2, 3}) {
		t.Fatal("handler did not receive complete payload")
	}
	fallback := []byte{8, 5, 6}
	if err := c.handleDatagramFrame(&wire.DatagramFrame{Data: fallback}); err != nil {
		t.Fatal(err)
	}
	clear(fallback)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.datagramQueue.Receive(ctx)
	if err != nil || !bytes.Equal(got, []byte{8, 5, 6}) {
		t.Fatal("fallthrough ownership or dispatch changed", got, err)
	}
	if err := c.SetDatagramReceiveHandler(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.handleDatagramFrame(&wire.DatagramFrame{Data: []byte{7, 9}}); err != nil {
		t.Fatal(err)
	}
	got, err = c.datagramQueue.Receive(ctx)
	if err != nil || !bytes.Equal(got, []byte{7, 9}) {
		t.Fatal("removing handler did not restore ordinary delivery", got, err)
	}
}

func TestDatagramReceiveHandlerCannotBypassFrameLimits(t *testing.T) {
	c := receiveHandlerTestConn()
	calls := 0
	if err := c.SetDatagramReceiveHandler(func([]byte) bool { calls++; return true }); err != nil {
		t.Fatal(err)
	}
	if err := c.handleDatagramFrame(&wire.DatagramFrame{Data: make([]byte, int(wire.MaxDatagramSize)+1)}); err == nil || calls != 0 {
		t.Fatal("handler bypassed QUIC frame-size validation")
	}
	if err := (&Conn{}).SetDatagramReceiveHandler(func([]byte) bool { return true }); err == nil {
		t.Fatal("enabled receiver without negotiated datagram machinery")
	}
}

func TestDatagramReceiveHandlerConcurrentReplacement(t *testing.T) {
	c := receiveHandlerTestConn()
	var calls atomic.Int64
	receiver := func([]byte) bool { calls.Add(1); return true }
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for range 300 {
			_ = c.SetDatagramReceiveHandler(receiver)
			_ = c.SetDatagramReceiveHandler(nil)
		}
	}()
	for range 300 {
		if err := c.handleDatagramFrame(&wire.DatagramFrame{Data: []byte{1, 2, 3}}); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
}
