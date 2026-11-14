package http3

import "github.com/quic-go/quic-go"

const (
	maxEarlyDatagrams     = 64
	maxEarlyDatagramBytes = 128 << 10
	// One fixed bitmap records the most recent 64 request-stream IDs.
	earlyTrackedStreamWindow = 64
)

// earlyDatagrams holds HTTP Datagrams that arrive before their request stream
// is tracked. A client may send datagrams right behind the request (RFC 9297
// section 5 allows a receiver to buffer them, and optimistic senders such as
// CONNECT-IP and CONNECT-UDP clients rely on that): the request STREAM frame
// and the first DATAGRAM frames are then handled by two different goroutines
// and the datagram can win the race. Dropping it costs the application a
// retransmission timeout on the very first packet of a tunnel.
//
// The buffer is deliberately small and bounded in both count and bytes. Only
// Request streams not known to have been tracked remain eligible. The oldest
// datagrams are kept when it is full; entries outside a fixed recent-stream
// window are discarded. A higher
// tracked ID does not imply that lower IDs were skipped: request handlers
// can register out of order. Recently tracked IDs are remembered so late
// datagrams for completed streams do not refill the cache.
//
// Only the side that accepts request streams holds anything: a client opens
// its streams itself, so a datagram for a stream it does not track belongs to
// a stream that has finished or was never opened.
//
// It is not safe for concurrent use; rawConn guards it with streamMx.
type earlyDatagrams struct {
	enabled        bool
	entries        []earlyDatagram
	bytes          int
	highestTracked quic.StreamID
	tracked        bool
	trackedMask    uint64 // bit 0 is highestTracked, bit 1 is the preceding request ID
}

type earlyDatagram struct {
	id   quic.StreamID
	data []byte
}

// hold keeps data for a stream that is not tracked yet. It reports whether
// the datagram was retained; the caller must not reuse data when it was.
func (e *earlyDatagrams) hold(id quic.StreamID, data []byte) bool {
	// A higher handler may register before a lower accepted stream. Reject
	// only IDs known to have been tracked or outside our bounded history.
	if !e.enabled || id%4 != 0 || e.unavailable(id) {
		return false
	}
	if len(e.entries) >= maxEarlyDatagrams || len(data) > maxEarlyDatagramBytes-e.bytes {
		return false
	}
	e.entries = append(e.entries, earlyDatagram{id: id, data: data})
	e.bytes += len(data)
	return true
}

// take records that id is now tracked and returns its held datagrams in
// arrival order. Entries outside the bounded registration history are released.
func (e *earlyDatagrams) take(id quic.StreamID) [][]byte {
	if !e.tracked {
		e.highestTracked, e.tracked, e.trackedMask = id, true, 1
	} else if id > e.highestTracked {
		shift := (id - e.highestTracked) / 4
		if shift >= earlyTrackedStreamWindow {
			e.trackedMask = 0
		} else {
			e.trackedMask <<= shift
		}
		e.highestTracked = id
		e.trackedMask |= 1
	} else if offset := (e.highestTracked - id) / 4; offset < earlyTrackedStreamWindow {
		e.trackedMask |= uint64(1) << offset
	}
	if len(e.entries) == 0 {
		return nil
	}
	var out [][]byte
	kept := e.entries[:0]
	for _, entry := range e.entries {
		switch {
		case entry.id == id:
			out = append(out, entry.data)
			e.bytes -= len(entry.data)
		case e.unavailable(entry.id):
			e.bytes -= len(entry.data)
		default:
			kept = append(kept, entry)
		}
	}
	clear(e.entries[len(kept):])
	e.entries = kept
	return out
}

// unavailable rejects previously tracked streams (including closed streams)
// and IDs older than the fixed bitmap. Unseen lower IDs inside the window
// remain eligible even when a later handler registered first.
func (e *earlyDatagrams) unavailable(id quic.StreamID) bool {
	if !e.tracked || id > e.highestTracked {
		return false
	}
	offset := (e.highestTracked - id) / 4
	return offset >= earlyTrackedStreamWindow || e.trackedMask&(uint64(1)<<offset) != 0
}
