package quic

import (
	"context"
	"net"
)

// PacketBatchWriter is an optional capability of a non-OOB net.PacketConn.
// Each slice is one already-protected UDP datagram, not a GSO super-packet.
// All slices are borrowed only until return and target the same address.
// On error some datagrams may have been sent, so callers must not retry the
// whole batch. Implementations must preserve datagram boundaries and deadlines.
// This interface neither enables nor advertises GSO, ECN or DF support.
type PacketBatchWriter interface {
	WritePacketBatch(packets [][]byte, addr net.Addr) error
}

// ContextPacketBatchWriter permits a bounded, backpressured relay writer. The
// context is canceled when this connection's send queue closes. Cancellation
// must unblock a pending write without closing a shared PacketConn or replaying
// a partially sent batch. Ordinary PacketBatchWriter implementations are unchanged.
type ContextPacketBatchWriter interface {
	PacketBatchWriter
	WritePacketBatchContext(context.Context, [][]byte, net.Addr) error
}

// PacketBatchSizer optionally opts a portable writer into larger ready batches.
// Values from 8 through 32 are supported. Other values retain the historical
// limit of 8. This does not wait for packets or bypass congestion pacing.
type PacketBatchSizer interface {
	PacketBatchWriter
	MaxPacketBatchSize() int
}

func (c *sconn) batchSize() int {
	if basic, ok := c.rawConn.(*basicConn); ok {
		if writer, ok := basic.PacketConn.(PacketBatchSizer); ok {
			return writer.MaxPacketBatchSize()
		}
	}
	return sendQueueCapacity
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

func (c *sconn) contextBatchWriter() func(context.Context, [][]byte) error {
	basic, ok := c.rawConn.(*basicConn)
	if !ok {
		return nil
	}
	writer, ok := basic.PacketConn.(ContextPacketBatchWriter)
	if !ok {
		return nil
	}
	return func(ctx context.Context, packets [][]byte) error {
		target := c.remoteAddrInfo.Load()
		return writer.WritePacketBatchContext(ctx, packets, target.addr)
	}
}
