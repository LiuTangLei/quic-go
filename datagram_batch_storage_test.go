package quic

import (
	"bytes"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

func TestDatagramBatchStorageSurvivesQueueRemoval(t *testing.T) {
	c := prefixTestConn(1400)
	payloads := [][]byte{nil, {7}, bytes.Repeat([]byte{9}, 1200)}
	prefix := []byte{1, 2}
	if n, err := c.SendDatagramsWithPrefix(prefix, payloads); n != 3 || err != nil {
		t.Fatalf("accepted %d: %v", n, err)
	}
	retained := make([]*wire.DatagramFrame, len(payloads))
	for i := range payloads {
		retained[i] = c.datagramQueue.Peek()
		c.datagramQueue.Pop()
		if cap(retained[i].Data) != len(retained[i].Data) {
			t.Fatal("payload can append into a neighboring datagram")
		}
	}
	clear(prefix)
	for _, p := range payloads {
		clear(p)
	}
	// Packet packing can retain frames after queue Pop. Subsequent batches
	// must not reuse their storage or the original caller's byte slices.
	for range 8 {
		if _, err := c.SendDatagramsWithPrefix([]byte{55}, [][]byte{bytes.Repeat([]byte{66}, 1200)}); err != nil {
			t.Fatal(err)
		}
		c.datagramQueue.Pop()
	}
	want := [][]byte{{1, 2}, {1, 2, 7}, append([]byte{1, 2}, bytes.Repeat([]byte{9}, 1200)...)}
	for i, f := range retained {
		if !bytes.Equal(f.Data, want[i]) || !f.DataLenPresent {
			t.Fatal("retained frame changed after queue removal")
		}
	}
	retained[0].Data = append(retained[0].Data, 42)
	if !bytes.Equal(retained[1].Data, want[1]) {
		t.Fatal("append crossed a datagram boundary")
	}
}

func TestDatagramBatchStorageAcrossBoundedGroups(t *testing.T) {
	ready := make(chan struct{}, 1)
	c := prefixTestConn(1400)
	c.datagramQueue = newDatagramQueue(func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	}, utils.DefaultLogger)
	packets := make([][]byte, 128)
	for i := range packets {
		packets[i] = bytes.Repeat([]byte{byte(i)}, 10+i)
	}
	done := make(chan error, 1)
	go func() {
		n, err := c.SendDatagramsWithPrefix([]byte{3}, packets)
		if n != len(packets) && err == nil {
			err = &DatagramTooLargeError{}
		}
		done <- err
	}()
	deadline := time.After(time.Second)
	for i := range packets {
		var f *wire.DatagramFrame
		for f == nil {
			f = c.datagramQueue.Peek()
			if f == nil {
				select {
				case <-ready:
				case <-deadline:
					t.Fatal("sender did not resume after queue space became available")
				}
			}
		}
		if !bytes.Equal(f.Data, append([]byte{3}, packets[i]...)) {
			t.Fatalf("packet %d corrupted/reordered", i)
		}
		c.datagramQueue.Pop()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
