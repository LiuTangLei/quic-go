package http3

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/require"
)

func TestEarlyDatagramsDeliveredInOrderOnTracking(t *testing.T) {
	e := earlyDatagrams{enabled: true}
	for i := range 3 {
		if !e.hold(0, []byte{byte(i)}) {
			t.Fatalf("datagram %d for the first request stream was not held", i)
		}
	}
	if !e.hold(4, []byte{9}) {
		t.Fatal("datagram for a later request stream was not held")
	}
	got := e.take(0)
	if len(got) != 3 || got[0][0] != 0 || got[1][0] != 1 || got[2][0] != 2 {
		t.Fatalf("held datagrams were lost or reordered: %v", got)
	}
	if again := e.take(0); again != nil {
		t.Fatalf("datagrams delivered twice: %v", again)
	}
	if got := e.take(4); len(got) != 1 || got[0][0] != 9 {
		t.Fatalf("later stream lost its datagram: %v", got)
	}
	if e.bytes != 0 || len(e.entries) != 0 {
		t.Fatalf("buffer accounting leaked: %d bytes, %d entries", e.bytes, len(e.entries))
	}
}

func TestEarlyDatagramsRejectPreviouslyTrackedStreams(t *testing.T) {
	e := earlyDatagrams{enabled: true}
	for _, id := range []quic.StreamID{0, 4, 8} {
		e.take(id)
	}
	for _, id := range []quic.StreamID{0, 4, 8, 1, 2, 3, 13} {
		require.False(t, e.hold(id, []byte{1}), "tracked or invalid stream %d", id)
	}
	require.True(t, e.hold(12, []byte{1}))
	require.True(t, e.hold(16, []byte{2}))
	require.True(t, e.hold(20, []byte{3}))
	require.Equal(t, [][]byte{{2}}, e.take(16))
	require.Equal(t, [][]byte{{1}}, e.take(12), "lower accepted stream was not skipped")
	require.Equal(t, [][]byte{{3}}, e.take(20))
	require.Zero(t, e.bytes)
	require.Empty(t, e.entries)
	require.False(t, e.hold(12, []byte{9}), "finished out-of-order stream must not refill cache")
}

func TestEarlyDatagramsBoundedRegistrationWindow(t *testing.T) {
	e := earlyDatagrams{enabled: true}
	require.True(t, e.hold(0, []byte{0}))
	require.True(t, e.hold(4, []byte{1}))
	e.take(4 * 64)
	require.False(t, e.hold(0, []byte{2}), "ID outside fixed history must stay expired")
	require.Equal(t, [][]byte{{1}}, e.take(4), "oldest eligible ID remains available")
	require.False(t, e.hold(4, []byte{3}), "tracked window-edge ID must stay retired")
	require.Zero(t, e.bytes)
	require.Empty(t, e.entries)
	e.take(4 * (64 + 1))
	require.False(t, e.hold(4, []byte{4}))
	require.True(t, e.hold(8, []byte{5}))
	require.Equal(t, [][]byte{{5}}, e.take(8))
	// A sparse jump clears obsolete bitmap bits, without allocating history.
	e.take(4096)
	require.False(t, e.hold(4, []byte{6}))
	require.True(t, e.hold(4092, []byte{7}))
	require.Equal(t, [][]byte{{7}}, e.take(4092))
}

func TestEarlyDatagramsAreBounded(t *testing.T) {
	e := earlyDatagrams{enabled: true}
	for i := range maxEarlyDatagrams + 10 {
		held := e.hold(0, []byte{byte(i)})
		if held != (i < maxEarlyDatagrams) {
			t.Fatalf("datagram %d: held=%v", i, held)
		}
	}
	// The oldest datagrams, which include a tunnel's first packet, are kept.
	if got := e.take(0); len(got) != maxEarlyDatagrams || got[0][0] != 0 {
		t.Fatalf("kept %d datagrams, first %v", len(got), got[0])
	}
	byBytes := earlyDatagrams{enabled: true}
	large := bytes.Repeat([]byte{1}, maxEarlyDatagramBytes/2+1)
	if !byBytes.hold(0, large) {
		t.Fatal("first large datagram must fit")
	}
	if byBytes.hold(0, large) {
		t.Fatal("byte bound was not enforced")
	}
	if !byBytes.hold(0, []byte{1}) {
		t.Fatal("a small datagram still fits under the byte bound")
	}
}

func TestEarlyDatagramsOnlyHeldByAcceptingSide(t *testing.T) {
	var client earlyDatagrams
	require.False(t, client.hold(0, []byte("for a stream this side never opened")))
	require.Nil(t, client.take(0))
}

// A datagram that arrives before the server tracks its request stream is
// delivered once the stream is tracked, ahead of later datagrams.
func TestServerDeliversDatagramThatPrecedesItsStream(t *testing.T) {
	clientConn, serverConn := newConnPair(t, withDatagrams())
	conn := newRawConn(serverConn, true, nil, nopControlStrHandler, nil, nil)
	conn.early.enabled = true // as newServerConn does

	b := quicvarint.Append(nil, streamTypeControlStream)
	b = (&settingsFrame{Datagram: true}).Append(b)
	controlStr, err := clientConn.OpenUniStream()
	require.NoError(t, err)
	_, err = controlStr.Write(b)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverStr, err := serverConn.AcceptUniStream(ctx)
	require.NoError(t, err)
	go conn.handleUnidirectionalStream(serverStr, true)

	quarterStreamID := quicvarint.Append([]byte{}, 0)
	require.NoError(t, clientConn.SendDatagram(append(quarterStreamID, []byte("early")...)))
	time.Sleep(scaleDuration(10 * time.Millisecond)) // let it arrive before the stream exists

	str, err := clientConn.OpenStream()
	require.NoError(t, err)
	_, err = str.Write([]byte("request"))
	require.NoError(t, err)
	accepted, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	require.Equal(t, quic.StreamID(0), accepted.StreamID())
	datagramStr := conn.TrackStream(accepted)
	require.NoError(t, clientConn.SendDatagram(append(quarterStreamID, []byte("late")...)))

	data, err := datagramStr.ReceiveDatagram(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("early"), data)
	data, err = datagramStr.ReceiveDatagram(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("late"), data)
}

// Server accepts stream 0 then 4, but starts both handlers as goroutines.
// The second goroutine may reach TrackStream first. A higher registered ID
// therefore does not prove that an accepted lower ID was skipped or closed.
func TestAcceptedLowerStreamEarlyDatagramSurvivesLaterHandler(t *testing.T) {
	client, server := newConnPair(t, withDatagrams())
	conn := newRawConn(server, true, func() {}, nopControlStrHandler, nil, nil)
	conn.early.enabled = true
	for range 2 {
		str, err := client.OpenStream()
		require.NoError(t, err)
		_, err = str.Write([]byte("open"))
		require.NoError(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := server.AcceptStream(ctx)
	require.NoError(t, err)
	second, err := server.AcceptStream(ctx)
	require.NoError(t, err)
	require.Equal(t, quic.StreamID(0), first.StreamID())
	require.Equal(t, quic.StreamID(4), second.StreamID())
	conn.streamMx.Lock()
	require.True(t, conn.early.hold(first.StreamID(), []byte("first tunnel packet")))
	conn.streamMx.Unlock()
	// Deliberately choose the valid handler schedule that exposes the bug.
	later := conn.TrackStream(second)
	lower := conn.TrackStream(first)
	defer later.CancelRead(0)
	defer later.CancelWrite(0)
	defer lower.CancelRead(0)
	defer lower.CancelWrite(0)
	readCtx, readCancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer readCancel()
	data, err := lower.ReceiveDatagram(readCtx)
	if err != nil {
		t.Fatalf("early data discarded for accepted lower stream when higher handler ran first: %v", err)
	}
	require.Equal(t, []byte("first tunnel packet"), data)
}

func TestEarlyDatagramsArriveAfterLaterStreamWasTracked(t *testing.T) {
	e := earlyDatagrams{enabled: true}
	e.take(12)
	require.True(t, e.hold(4, []byte("early4")))
	require.True(t, e.hold(0, []byte("early0")))
	require.True(t, e.hold(8, []byte("early8")))
	for _, id := range []quic.StreamID{4, 8, 0} {
		require.Len(t, e.take(id), 1, "late registration of request %d", id)
		require.False(t, e.hold(id, []byte("late")), "completed request must not refill buffer")
	}
	require.Zero(t, e.bytes)
	require.Empty(t, e.entries)
}
