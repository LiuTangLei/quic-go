package quic

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
)

type blockedContextSendConn struct {
	sendConn
	entered chan struct{}
}

func (c *blockedContextSendConn) contextBatchWriter() func(context.Context, [][]byte) error {
	return func(ctx context.Context, _ [][]byte) error {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
}
func (c *blockedContextSendConn) Write([]byte, uint16, protocol.ECN) error {
	return errors.New("context-aware single write bypassed cancellation")
}
func TestSendQueueCloseCancelsBackpressuredWriter(t *testing.T) {
	for _, count := range []int{1, sendQueueCapacity} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c := &blockedContextSendConn{entered: make(chan struct{}, 1)}
			q := newSendQueue(c)
			for i := 0; i < count; i++ {
				b := getPacketBuffer()
				b.Data = append(b.Data[:0], byte(i))
				q.Send(b, 0, protocol.ECNUnsupported)
			}
			result := make(chan error, 1)
			go func() { result <- q.Run() }()
			select {
			case <-c.entered:
			case <-time.After(time.Second):
				t.Fatal("did not enter context writer")
			}
			// Include accepted packets which have not been dequeued yet.
			for range sendQueueCapacity {
				b := getPacketBuffer()
				b.Data = append(b.Data[:0], 42)
				q.Send(b, 0, protocol.ECNUnsupported)
			}
			closed := make(chan struct{})
			go func() { q.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("close waited forever on relay queue")
			}
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if len(q.queue) != 0 {
				t.Fatal("canceled close retained queued packet storage")
			}
		})
	}
}
