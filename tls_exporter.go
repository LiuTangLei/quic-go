package quic

import "errors"

// ExportKeyingMaterial exports RFC 5705 / TLS 1.3 channel-binding material
// from the actual negotiated handshake, including a customized client path.
// Use this instead of ConnectionState().TLS.ExportKeyingMaterial when a
// ClientHelloProfile is enabled: the public crypto/tls state cannot hold
// another implementation's private exporter without unsafe type manipulation.
func (c *Conn) ExportKeyingMaterial(label string, context []byte, length int) ([]byte, error) {
	c.connStateMutex.Lock()
	state := c.cryptoStreamHandler.ConnectionState()
	c.connStateMutex.Unlock()
	if !state.HandshakeComplete {
		return nil, errors.New("TLS handshake is not complete")
	}
	if state.Exporter != nil {
		return state.Exporter(label, context, length)
	}
	return state.ConnectionState.ExportKeyingMaterial(label, context, length)
}
