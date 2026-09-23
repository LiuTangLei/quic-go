// Copyright (c) QUIC fork contributors.
// SPDX-License-Identifier: MIT

package quic

import (
	"bytes"
	"fmt"
	"testing"
)

// Exercise the same public batch API before and after allocation changes.
// The queue drains every iteration. This isolates allocations/copy/queue cost;
// it is not an encrypted network throughput benchmark.
func BenchmarkDatagramBatchAllocation(b *testing.B) {
	for _, size := range []int{64, 1200} {
		for _, count := range []int{1, 8, 32} {
			b.Run(fmt.Sprintf("bytes=%d/batch=%d", size, count), func(b *testing.B) {
				c := prefixTestConn(1400)
				packets := make([][]byte, count)
				for i := range packets {
					packets[i] = bytes.Repeat([]byte{byte(i)}, size)
				}
				prefix := []byte{0, 0}
				// Initialize the bounded queue before timed iterations.
				if _, err := c.SendDatagramsWithPrefix(prefix, packets); err != nil {
					b.Fatal(err)
				}
				for range packets {
					c.datagramQueue.Pop()
				}
				b.ReportAllocs()
				b.SetBytes(int64(count * size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n, err := c.SendDatagramsWithPrefix(prefix, packets)
					if err != nil || n != count {
						b.Fatalf("accepted %d: %v", n, err)
					}
					for range packets {
						c.datagramQueue.Pop()
					}
				}
			})
		}
	}
}
