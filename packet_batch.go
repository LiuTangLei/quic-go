package quic

import (
	"errors"
	"github.com/quic-go/quic-go/internal/protocol"
	"net"
)

const portablePacketBatchSize = 8

// PacketBatchWriter is an optional capability of a non-OOB net.PacketConn.
// Each slice is one already-protected UDP datagram, not a GSO super-packet.
// All slices are borrowed only until return and target the same address.
// On error some datagrams may have been sent, so callers must not retry the
// whole batch. Implementations must preserve datagram boundaries and deadlines.
// This interface neither enables nor advertises GSO, ECN or DF support.
type PacketBatchWriter interface {
	WritePacketBatch(packets [][]byte, addr net.Addr) error
}

func (c *sconn) writePortablePacketBatch(p []byte, addr net.Addr, segmentSize uint16, ecn protocol.ECN) error {
	if segmentSize == 0 || ecn != protocol.ECNUnsupported || len(p) > portablePacketBatchSize*int(segmentSize) {
		return errors.New("invalid portable UDP packet batch")
	}
	basic, ok := c.rawConn.(*basicConn)
	if !ok {
		return errors.New("portable batch requires a basic connection")
	}
	writer, ok := basic.PacketConn.(PacketBatchWriter)
	if !ok {
		return errors.New("portable batch writer unavailable")
	}
	var packets [portablePacketBatchSize][]byte
	n := 0
	for len(p) > 0 {
		size := min(len(p), int(segmentSize))
		packets[n] = p[:size]
		n++
		p = p[size:]
	}
	if n == 0 {
		return nil
	}
	return writer.WritePacketBatch(packets[:n], addr)
}

// Only the basic (non-OOB) path can use this portable batching capability.
// Kernel GSO continues to use its existing path. The remote endpoint snapshot
// is taken once per batch, as it is for one ordinary Write call.
func (c *sconn) batchWriter() func([][]byte) error {
	basic, ok := c.rawConn.(*basicConn)
	if !ok {
		return nil
	}
	writer, ok := basic.PacketConn.(PacketBatchWriter)
	if !ok {
		return nil
	}
	return func(packets [][]byte) error {
		target := c.remoteAddrInfo.Load()
		return writer.WritePacketBatch(packets, target.addr)
	}
}
