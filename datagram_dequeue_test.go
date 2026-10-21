package quic

import (
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestDatagramDrainRetainsACKBlockedHead(t *testing.T) {
	q := newDatagramQueue(func() {}, utils.DefaultLogger, nil)
	head := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 1000)}
	require.NoError(t, q.Add(head))
	ack := &wire.AckFrame{}
	pl := q.appendDatagrams(payload{ack: ack, length: 30}, 1020, maxDatagramSendQueueLen, protocol.Version1)
	require.Empty(t, pl.frames)
	require.Equal(t, protocol.ByteCount(30), pl.length)
	require.Same(t, head, q.Peek())
	select {
	case <-q.sent:
		t.Fatal("notified a producer without removing anything")
	default:
	}
	// The same head fits the next packet; ownership moves to the payload.
	pl = q.appendDatagrams(payload{}, 1020, maxDatagramSendQueueLen, protocol.Version1)
	require.Len(t, pl.frames, 1)
	require.Same(t, head, pl.frames[0].Frame)
	require.Nil(t, q.Peek())
}

func TestDatagramDrainWakesPartialBatchProducer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDatagramQueue(func() {}, utils.DefaultLogger, nil)
		for i := range maxDatagramSendQueueLen {
			require.NoError(t, q.Add(&wire.DatagramFrame{DataLenPresent: true, Data: []byte{byte(i)}}))
		}
		type result struct {
			n   int
			err error
		}
		done := make(chan result, 1)
		go func() {
			var frames [4]*wire.DatagramFrame
			for i := range frames {
				frames[i] = &wire.DatagramFrame{DataLenPresent: true, Data: []byte{byte(32 + i)}}
			}
			n, err := q.addBatch(frames[:])
			done <- result{n, err}
		}()
		synctest.Wait()
		for batch := range 2 {
			pl := q.appendDatagrams(payload{}, 1400, 2, protocol.Version1)
			require.Len(t, pl.frames, 2)
			for i, frame := range pl.frames {
				require.Equal(t, []byte{byte(batch*2 + i)}, frame.Frame.(*wire.DatagramFrame).Data)
			}
			synctest.Wait()
			require.Equal(t, maxDatagramSendQueueLen, q.sendQueue.Len())
			if batch == 0 {
				select {
				case <-done:
					t.Fatal("producer completed without four available slots")
				default:
				}
			}
		}
		r := <-done
		require.NoError(t, r.err)
		require.Equal(t, 4, r.n)
		pl := q.appendDatagrams(payload{}, 1400, maxDatagramSendQueueLen, protocol.Version1)
		require.Len(t, pl.frames, maxDatagramSendQueueLen)
		for i, frame := range pl.frames {
			require.Equal(t, []byte{byte(4 + i)}, frame.Frame.(*wire.DatagramFrame).Data)
		}
		require.Nil(t, q.Peek())
	})
}
