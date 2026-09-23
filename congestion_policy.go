package quic

// EnableCubicCongestionControl is a source-compatibility alias for old callers.
// Deprecated: this fork uses lightly tuned BBRv3 for every connection. This
// method does not select CUBIC and does not change the wire protocol.
func (c *Config) EnableCubicCongestionControl() {
	c.EnableBBRv3CongestionControl()
	c.EnableCubic = true // retained request bit, not the active algorithm
}
