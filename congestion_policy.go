package quic

// EnableCubicCongestionControl selects the fork's opt-in CUBIC controller.
// It must be called before the config is passed to Dial or Listen.
// No congestion, pacing, loss detection or TLS checks are disabled.
func (c *Config) EnableCubicCongestionControl() { c.EnableCubic = true }
