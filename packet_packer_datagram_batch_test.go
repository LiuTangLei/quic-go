// Copyright (c) QUIC fork contributors.
// SPDX-License-Identifier: MIT

package quic

import (
	"bytes"
	"testing"

	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func datagramBatchPacker(t *testing.T, hasReliable bool) *testPacketPacker {
	ctrl := gomock.NewController(t)
	tp := newTestPacketPacker(t, ctrl, protocol.PerspectiveServer)
	tp.ackFramer.EXPECT().GetAckFrame(protocol.Encryption1RTT, gomock.Any(), gomock.Any()).AnyTimes()
	tp.pnManager.EXPECT().PeekPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(1), protocol.PacketNumberLen2).AnyTimes()
	tp.pnManager.EXPECT().PopPacketNumber(protocol.PacketNumberSpaceAppData).Return(protocol.PacketNumber(1)).AnyTimes()
	tp.sealingManager.EXPECT().Get1RTTSealer().Return(newMockShortHeaderSealer(ctrl), nil).AnyTimes()
	tp.framer.EXPECT().HasData().Return(hasReliable).AnyTimes()
	if hasReliable {
		tp.framer.EXPECT().Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(f []ackhandler.Frame, s []ackhandler.StreamFrame, _ protocol.ByteCount, _ monotime.Time, _ protocol.Version) ([]ackhandler.Frame, []ackhandler.StreamFrame, protocol.ByteCount) {
			return append(f, ackhandler.Frame{Frame: &wire.PingFrame{}}), s, 1
		}).AnyTimes()
	}
	return tp
}

func TestPackReadyDatagramsTogetherWithoutCrossingBoundaries(t *testing.T) {
	tp := datagramBatchPacker(t, false)
	for i := range 8 {
		tp.datagramQueue.Add(&wire.DatagramFrame{DataLenPresent: true, Data: bytes.Repeat([]byte{byte(i)}, 64)})
	}
	buffer := getPacketBuffer()
	defer buffer.Release()
	p, e := tp.packer.AppendPacket(buffer, 1200, monotime.Now(), protocol.Version1)
	require.NoError(t, e)
	require.Len(t, p.Frames, 8)
	require.LessOrEqual(t, len(buffer.Data), 1200)
	for i, f := range p.Frames {
		require.Equal(t, bytes.Repeat([]byte{byte(i)}, 64), f.Frame.(*wire.DatagramFrame).Data)
		require.Nil(t, f.Handler)
	}
	require.Nil(t, tp.datagramQueue.Peek())
}

func TestPackDatagramsKeepNextFrameThatDoesNotFitRemainder(t *testing.T) {
	tp := datagramBatchPacker(t, false)
	first := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 700)}
	next := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 800)}
	tp.datagramQueue.Add(first)
	tp.datagramQueue.Add(next)
	buffer := getPacketBuffer()
	defer buffer.Release()
	p, e := tp.packer.AppendPacket(buffer, 1200, monotime.Now(), protocol.Version1)
	require.NoError(t, e)
	require.Len(t, p.Frames, 1)
	require.Same(t, next, tp.datagramQueue.Peek())
	second := getPacketBuffer()
	defer second.Release()
	p, e = tp.packer.AppendPacket(second, 1200, monotime.Now(), protocol.Version1)
	require.NoError(t, e)
	require.Len(t, p.Frames, 1)
	require.Same(t, next, p.Frames[0].Frame)
}

func TestPackDatagramsPreserveReliableDataOpportunity(t *testing.T) {
	tp := datagramBatchPacker(t, true)
	for range 2 {
		tp.datagramQueue.Add(&wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 64)})
	}
	buffer := getPacketBuffer()
	defer buffer.Release()
	p, e := tp.packer.AppendPacket(buffer, 1200, monotime.Now(), protocol.Version1)
	require.NoError(t, e)
	require.Len(t, p.Frames, 2)
	datagrams, pings := 0, 0
	for _, f := range p.Frames {
		switch f.Frame.(type) {
		case *wire.DatagramFrame:
			datagrams++
		case *wire.PingFrame:
			pings++
		}
	}
	require.Equal(t, 1, datagrams)
	require.Equal(t, 1, pings)
	require.NotNil(t, tp.datagramQueue.Peek())
}

func TestPackDatagramsDropsOnlyAnOversizedEmptyPacketHead(t *testing.T) {
	tp := datagramBatchPacker(t, false)
	tp.datagramQueue.Add(&wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 2000)})
	next := &wire.DatagramFrame{DataLenPresent: true, Data: make([]byte, 64)}
	tp.datagramQueue.Add(next)
	buffer := getPacketBuffer()
	defer buffer.Release()
	p, e := tp.packer.AppendPacket(buffer, 1200, monotime.Now(), protocol.Version1)
	require.NoError(t, e)
	require.Len(t, p.Frames, 1)
	require.Same(t, next, p.Frames[0].Frame)
	require.Nil(t, tp.datagramQueue.Peek())
}

func TestDatagramBatchUsesImmutablePublishedParameters(t *testing.T) {
	c := prefixTestConn(1400)
	params := *c.appliedPeerParams.Load()
	params.MaxDatagramFrameSize = 64
	c.appliedPeerParams.Store(&params)
	n, e := c.SendDatagramsWithPrefix(nil, [][]byte{make([]byte, 100)})
	var large *DatagramTooLargeError
	require.ErrorAs(t, e, &large)
	require.Zero(t, n)
	require.Nil(t, c.datagramQueue.Peek())
	c.peerParams = nil // the public API must not read mutable handshake storage
	n, e = c.SendDatagramsWithPrefix(nil, [][]byte{{1, 2, 3}})
	require.NoError(t, e)
	require.Equal(t, 1, n)
}
