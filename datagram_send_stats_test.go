package quic

import (
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

func TestDatagramSendWaitStats(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			c := prefixTestConn(1400)
			var stats utils.ConnectionStats
			stats.BBRState.Store(7) // probe_rtt
			c.datagramQueue.connStats = &stats
			for range maxDatagramSendQueueLen {
				if err := c.datagramQueue.Add(&wire.DatagramFrame{Data: []byte{0}}); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				if batch {
					_, err := c.SendDatagramsWithPrefix(nil, [][]byte{{1}})
					done <- err
				} else {
					done <- c.datagramQueue.Add(&wire.DatagramFrame{Data: []byte{1}})
				}
			}()
			deadline := time.Now().Add(time.Second)
			for c.DatagramSendQueueStats().ByBBRState[7].Waits == 0 {
				if time.Now().After(deadline) {
					t.Fatal("sender did not block")
				}
				time.Sleep(time.Millisecond)
			}
			// Mode changes during a wait must not misattribute it to the exit mode.
			stats.BBRState.Store(1)
			failure := errors.New("cancel full queue")
			c.datagramQueue.CloseWithError(failure)
			select {
			case err := <-done:
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("sender did not cancel")
			}
			got := c.DatagramSendQueueStats()
			v := got.ByBBRState[7]
			wantBatch := uint64(0)
			if batch {
				wantBatch = 1
			}
			if got.Queued != 32 || v.BBRState != "probe_rtt" || v.Waits != 1 || v.BatchWaits != wantBatch || v.Nanoseconds == 0 || v.MaxNanoseconds != v.Nanoseconds || got.ByBBRState[1].Waits != 0 {
				t.Fatalf("unexpected wait snapshot: %+v", got)
			}
		})
	}
}
