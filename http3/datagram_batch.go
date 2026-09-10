package http3

import (
	"errors"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

// SendDatagramsWithPrefix sends independent HTTP Datagrams, adding prefix to
// each payload. Return count identifies the accepted prefix of the batch.
// This method is opt-in; ordinary HTTP/3 callers retain SendDatagram behavior.
func (s *Stream) SendDatagramsWithPrefix(prefix []byte, packets [][]byte) (int, error) {
	if sender, ok := s.datagramStream.(interface {
		SendDatagramsWithPrefix([]byte, [][]byte) (int, error)
	}); ok {
		return sender.SendDatagramsWithPrefix(prefix, packets)
	}
	return 0, errors.New("HTTP Datagram batch sender unavailable")
}
func (s *RequestStream) SendDatagramsWithPrefix(prefix []byte, packets [][]byte) (int, error) {
	return s.str.SendDatagramsWithPrefix(prefix, packets)
}
func (s *stateTrackingStream) SendDatagramsWithPrefix(prefix []byte, packets [][]byte) (int, error) {
	s.mx.Lock()
	err := s.sendErr
	s.mx.Unlock()
	if err != nil {
		return 0, err
	}
	if s.sendDatagramBatch == nil {
		return 0, errors.New("HTTP Datagram batch sender unavailable")
	}
	return s.sendDatagramBatch(prefix, packets)
}
func (c *rawConn) sendDatagramBatch(streamID quic.StreamID, prefix []byte, packets [][]byte) (int, error) {
	// A bounded prefix prevents this convenience API becoming an unbounded
	// allocation path. CONNECT-IP needs only its one-byte context ID.
	if len(prefix) > 8 {
		return 0, errors.New("HTTP Datagram batch prefix exceeds 8 bytes")
	}
	var header [16]byte
	quarter := uint64(streamID / 4)
	h := quicvarint.Append(header[:0], quarter)
	h = append(h, prefix...)
	n, err := c.conn.SendDatagramsWithPrefix(h, packets)
	if c.qlogger != nil {
		for _, p := range packets[:n] {
			c.qlogger.RecordEvent(qlog.DatagramCreated{QuarterStreamID: quarter, Raw: qlog.RawInfo{Length: len(h) + len(p), PayloadLength: len(prefix) + len(p)}})
		}
	}
	return n, err
}
