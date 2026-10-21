package quic

import (
	"fmt"
	"testing"

	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

// Keep the previous packer loop as a microbenchmark control. Both variants
// enqueue the same owned frames and reuse packet descriptors. This measures
// queue work, not encryption, network throughput or UDP latency.
func legacyDatagramDrain(q *datagramQueue, pl payload) payload {
	for range maxDatagramSendQueueLen {
		f := q.Peek()
		if f == nil {
			break
		}
		size := f.Length(protocol.Version1)
		if size > 1400-pl.length {
			if pl.ack == nil && len(pl.frames) == 0 {
				q.Pop()
				continue
			}
			break
		}
		pl.frames = append(pl.frames, ackhandler.Frame{Frame: f})
		pl.length += size
		q.Pop()
	}
	return pl
}

func BenchmarkDatagramPacketDrain(b *testing.B) {
	for _, size := range []int{64, 1200} {
		for _, batched := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/batched=%t", size, batched), func(b *testing.B) {
				q := newDatagramQueue(func() {}, utils.DefaultLogger, nil)
				var frames [maxDatagramSendQueueLen]*wire.DatagramFrame
				for i := range frames {
					frames[i] = &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, size)}
				}
				q.sendQueue.Init(maxDatagramSendQueueLen)
				storage := make([]ackhandler.Frame, 0, maxDatagramSendQueueLen)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := q.addBatch(frames[:]); err != nil {
						b.Fatal(err)
					}
					for remaining := len(frames); remaining > 0; {
						pl := payload{frames: storage[:0]}
						if batched {
							pl = q.appendDatagrams(pl, 1400, maxDatagramSendQueueLen, protocol.Version1)
						} else {
							pl = legacyDatagramDrain(q, pl)
						}
						remaining -= len(pl.frames)
					}
				}
			})
		}
	}
}
