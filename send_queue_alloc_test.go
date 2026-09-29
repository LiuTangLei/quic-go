package quic

import (
	"errors"
	"fmt"
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
)

func TestSendQueueScratchReleased(t *testing.T) {
	for _, failure := range []error{nil, errors.New("partial write")} {
		for _, count := range []int{1, 8, 32} {
			c := &sizedBatchTestConn{batchTestConn: &batchTestConn{failure: failure}, size: 32}
			q := newSendQueue(c).(*sendQueue)
			// Follow a large batch with a short one, including an error return.
			for _, n := range []int{32, count} {
				entries := make([]queueEntry, n)
				for i := range entries {
					p := getPacketBuffer()
					p.Data = append(p.Data[:0], byte(i))
					entries[i] = queueEntry{buf: p, ecn: protocol.ECNUnsupported}
				}
				if err := q.writeReadyBatch(entries); !errors.Is(err, failure) {
					t.Fatal(err)
				}
				for _, data := range q.batchData {
					if data != nil {
						t.Fatal("scratch retained a released packet")
					}
				}
			}
		}
	}
}

type discardBatchConn struct{ sendConn }

func (*discardBatchConn) Write([]byte, uint16, protocol.ECN) error { return nil }
func (*discardBatchConn) batchSize() int                           { return maxPortableBatchSize }
func (*discardBatchConn) batchWriter() func([][]byte) error {
	return func([][]byte) error { return nil }
}

func BenchmarkSendQueueReadyWrite(b *testing.B) {
	for _, count := range []int{1, 8, 32} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			q := newSendQueue(&discardBatchConn{}).(*sendQueue)
			entries := make([]queueEntry, count)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for i := range entries {
					p := getPacketBuffer()
					p.Data = p.Data[:1200]
					entries[i] = queueEntry{buf: p, ecn: protocol.ECNUnsupported}
				}
				if err := q.writeReadyBatch(entries); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
