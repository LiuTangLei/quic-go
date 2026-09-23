package quic

import "github.com/quic-go/quic-go/internal/protocol"

// The bandwidth sampler must distinguish an empty application from a slow
// network. This is especially important for a bidirectional IP tunnel: one
// direction can carry only small inner ACKs for seconds while the other is busy.
// Do not infer this from RTT or timer spacing: inspect the actual pending work.
func (c *Conn) maybeNotifyApplicationLimited() {
	// BBRv3 is unconditional; the old selector bits are not active policy.
	if !c.handshakeConfirmed {
		return
	}
	if c.framer != nil && c.framer.HasData() {
		return
	}
	if c.retransmissionQueue != nil && c.retransmissionQueue.HasData(protocol.Encryption1RTT) {
		return
	}
	if c.oneRTTStream != nil && c.oneRTTStream.HasData() {
		return
	}
	if c.datagramQueue != nil && c.datagramQueue.Peek() != nil {
		return
	}
	if h, ok := c.sentPacketHandler.(interface{ OnApplicationLimited() }); ok {
		h.OnApplicationLimited()
	}
}
