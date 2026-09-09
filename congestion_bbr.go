package quic

// EnableBBRCongestionControl selects this fork's opt-in BBRv1-derived controller
// with BBRv3-inspired startup, idle restart and RTT probing (see BBR.md).
// Call before Dial / Listen. It retains pacing, an in-flight window, loss
// detection and recovery; this is not a fixed-rate or congestion bypass mode.
func (c *Config) EnableBBRCongestionControl() {
	c.EnableBBR = true
	c.EnableBBRv3 = false
	c.EnableCubic = false
}

// EnableBBRv3CongestionControl selects a distinct opt-in BBRv3 controller.
// It keeps the legacy BBRv1 path available for compatibility, but only one
// controller may be active per connection.
func (c *Config) EnableBBRv3CongestionControl() {
	c.EnableBBRv3 = true
	c.EnableBBR = false
	c.EnableCubic = false
}

// CongestionControlName returns the identity used in connection diagnostics.
func (c *Config) CongestionControlName() string {
	if c == nil {
		return "reno"
	}
	if c.EnableCubic {
		return "cubic"
	}
	if c.EnableBBRv3 {
		return "bbr-v3"
	}
	if c.EnableBBR {
		return "bbr-v1"
	}
	return "reno"
}
