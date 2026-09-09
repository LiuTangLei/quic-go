package quic

import "net"

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
