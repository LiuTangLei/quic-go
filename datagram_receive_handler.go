package quic

import "errors"

type datagramReceiveHandler struct {
	receive func([]byte) bool
}

// SetDatagramReceiveHandler installs an optional receiver for authenticated
// QUIC DATAGRAM payloads. The handler runs in the connection receive loop and
// must not block or call methods that wait for that loop (including Close).
// Its slice is borrowed only during the call; retained bytes must be copied.
// Returning false sends the unchanged bytes through normal ReceiveDatagram.
// Returning true consumes the datagram. Transport size checks run first.
//
// This is local dispatch, not a negotiated wire capability. Application-level
// authorization remains the handler's responsibility. Replacing or removing a
// handler does not wait for an already-running call; users must handle their
// own revocation/lifecycle synchronization. nil restores the ordinary path.
func (c *Conn) SetDatagramReceiveHandler(handler func([]byte) bool) error {
	if handler == nil {
		c.datagramReceiver.Store(nil)
		return nil
	}
	if c.config == nil || !c.config.EnableDatagrams || c.datagramQueue == nil {
		return errors.New("datagram support disabled")
	}
	c.datagramReceiver.Store(&datagramReceiveHandler{receive: handler})
	return nil
}
