package quic

// EnableBBRCongestionControl selects this fork's opt-in BBRv1 controller.
// Call before Dial / Listen. It retains pacing, an in-flight window, loss
// detection and recovery; this is not a fixed-rate or congestion bypass mode.
func (c *Config) EnableBBRCongestionControl() { c.EnableBBR = true; c.EnableCubic = false }
