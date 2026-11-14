package quic

import (
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
)

// maxDatagramDataLen is the largest DATAGRAM payload that can be sent now.
//
// Until its handshake completes, a client sends application data in 0-RTT
// packets. Their long header is larger than the short header assumed by
// maxPayloadSizeEstimate, so a payload sized for a 1-RTT packet fits no 0-RTT
// packet at all. Reporting the smaller limit lets the application split what
// it sends early, instead of having it wait for the handshake.
func (c *Conn) maxDatagramDataLen(params *wire.TransportParameters) protocol.ByteCount {
	f := wire.DatagramFrame{DataLenPresent: true}
	limit := min(f.MaxDataLen(params.MaxDatagramFrameSize, c.version), protocol.ByteCount(c.maxPayloadSizeEstimate.Load()))
	if c.perspective != protocol.PerspectiveClient {
		return limit
	}
	select {
	case <-c.handshakeCompleteChan:
		return limit
	default:
		return limit - min(limit, earlyDatagramOverhead(c.srcConnIDLen))
	}
}

// earlyDatagramOverhead is how much less DATAGRAM payload a 0-RTT packet
// carries than maxPayloadSizeEstimate assumes: version (4), two connection ID
// length bytes (2), source connection ID, length (2), the largest packet
// number (4) and the DATAGRAM frame header (3). The destination connection ID
// is counted at its maximum length in both, since a Retry may replace it.
func earlyDatagramOverhead(srcConnIDLen int) protocol.ByteCount {
	return protocol.ByteCount(4 + 2 + srcConnIDLen + 2 + 4 + 3)
}
