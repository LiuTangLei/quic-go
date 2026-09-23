package quic

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/wire"
	"go.uber.org/mock/gomock"
)

func TestWaitWriteAcknowledgedRequiresPeerACK(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := NewMockStreamSender(gomock.NewController(t))
		str := newSendStream(t.Context(), 0, sender, nil, false)
		s := &Stream{sendStr: str}
		done := make(chan error, 1)
		go func() { done <- s.WaitWriteAcknowledged(t.Context()) }()
		assertWaiting := func() {
			t.Helper()
			synctest.Wait()
			select {
			case err := <-done:
				t.Fatalf("returned before peer ACK: %v", err)
			default:
			}
		}
		assertWaiting()
		sender.EXPECT().onHasStreamData(str.streamID, str)
		if err := str.Close(); err != nil {
			t.Fatal(err)
		}
		assertWaiting() // application Close / stream Context is not ACK
		str.mutex.Lock()
		str.finSent = true
		str.numOutstandingFrames = 1
		str.mutex.Unlock()
		assertWaiting() // FIN sent, still not acknowledged
		sender.EXPECT().onStreamCompleted(str.streamID)
		(*sendStreamAckHandler)(str).OnAcked(&wire.StreamFrame{Fin: true})
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestWaitWriteAcknowledgedCancellationAndReset(t *testing.T) {
	for _, mode := range []string{"context", "local-reset", "remote-reset", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sender := NewMockStreamSender(gomock.NewController(t))
				str := newSendStream(t.Context(), 0, sender, nil, false)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- (&Stream{sendStr: str}).WaitWriteAcknowledged(ctx) }()
				synctest.Wait()
				var want error
				switch mode {
				case "context":
					want = context.Canceled
					cancel()
				case "local-reset":
					sender.EXPECT().onHasStreamControlFrame(str.streamID, str)
					str.CancelWrite(42)
					want = str.resetErr
				case "remote-reset":
					sender.EXPECT().onHasStreamControlFrame(str.streamID, str)
					str.handleStopSendingFrame(&wire.StopSendingFrame{ErrorCode: 43})
					want = str.resetErr
				case "shutdown":
					want = errors.New("connection closed before ACK")
					str.closeForShutdown(want)
				}
				synctest.Wait()
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		})
	}
}

func TestWaitWriteAcknowledgedConcurrentAndLateWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		str := newSendStream(t.Context(), 0, nil, nil, false)
		s := &Stream{sendStr: str}
		done := make(chan error, 32)
		for range cap(done) {
			go func() { done <- s.WaitWriteAcknowledged(t.Context()) }()
		}
		synctest.Wait()
		str.mutex.Lock()
		str.finSent = true
		if !str.isNewlyCompleted() {
			t.Fatal("fixture failed to complete")
		}
		str.mutex.Unlock()
		synctest.Wait()
		for range cap(done) {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		// A subsequent connection shutdown cannot undo a real prior ACK.
		str.closeForShutdown(errors.New("later shutdown"))
		if err := s.WaitWriteAcknowledged(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWaitWriteAcknowledgedResetIsNotSuccess(t *testing.T) {
	str := newSendStream(t.Context(), 0, nil, nil, false)
	str.finAcknowledged = true
	str.resetErr = &StreamError{ErrorCode: 42}
	if err := (&Stream{sendStr: str}).WaitWriteAcknowledged(t.Context()); err == nil {
		t.Fatal("reset stream claimed acknowledgment")
	}
}

func BenchmarkWaitWriteAcknowledgedAlreadyComplete(b *testing.B) {
	str := newSendStream(context.Background(), 0, nil, nil, false)
	str.finSent = true
	str.isNewlyCompleted()
	s := &Stream{sendStr: str}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.WaitWriteAcknowledged(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
