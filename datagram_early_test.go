package quic

import (
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
)

// The largest payload offered to a client before its handshake completes
// must fit a 0-RTT packet of its own, whatever the connection ID lengths.
func TestEarlyDatagramLimitFitsZeroRTTPacket(t *testing.T) {
	const tag = 16
	for _, mtu := range []protocol.ByteCount{1200, 1252, 1320, 1452} {
		for _, srcLen := range []int{0, 4, 8, 20} {
			limit := estimateMaxPayloadSize(mtu) - earlyDatagramOverhead(srcLen)
			hdr := &wire.ExtendedHeader{
				Header: wire.Header{
					Type:             protocol.PacketType0RTT,
					Version:          protocol.Version1,
					DestConnectionID: protocol.ParseConnectionID(make([]byte, 20)),
					SrcConnectionID:  protocol.ParseConnectionID(make([]byte, srcLen)),
				},
				PacketNumberLen: protocol.PacketNumberLen4,
			}
			frame := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, limit)}
			size := hdr.GetLength(protocol.Version1) + frame.Length(protocol.Version1) + tag
			require.LessOrEqual(t, size, mtu, "mtu %d, source connection ID %d", mtu, srcLen)
			require.Greater(t, size, mtu-8, "limit wastes packet space")
		}
	}
}

// A frame that does not fit a packet made smaller by the handshake waits for
// the next packet. Only a full-size packet shows that the path cannot carry it.
func TestDatagramDrainKeepsFrameForNextPacketDuringHandshake(t *testing.T) {
	q := newDatagramQueue(func() {}, utils.DefaultLogger, nil)
	head := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 1000)}
	require.NoError(t, q.Add(head))
	// A 0-RTT packet that shares its UDP datagram with the Initial packet.
	pl := q.appendDatagrams(payload{}, 300, maxDatagramSendQueueLen, protocol.Version1, false)
	require.Empty(t, pl.frames)
	require.Same(t, head, q.Peek())
	select {
	case <-q.sent:
		t.Fatal("notified a producer without removing anything")
	default:
	}
	// The next 0-RTT packet has a UDP datagram to itself.
	pl = q.appendDatagrams(payload{}, 1100, maxDatagramSendQueueLen, protocol.Version1, false)
	require.Len(t, pl.frames, 1)
	require.Same(t, head, pl.frames[0].Frame)

	require.NoError(t, q.Add(&wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 1000)}))
	pl = q.appendDatagrams(payload{}, 300, maxDatagramSendQueueLen, protocol.Version1, true)
	require.Empty(t, pl.frames)
	require.Nil(t, q.Peek(), "a frame the path cannot carry must not block the queue")
}
