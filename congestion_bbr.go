package quic

// EnableBBRCongestionControl is retained for existing Tailscale integrations.
// Deprecated: lightly tuned BBRv3 is now always used, including zero Config.
func (c *Config) EnableBBRCongestionControl() {
	c.EnableBBRv3CongestionControl()
	// Some existing integrations inspect this legacy request bit before
	// Dial. Preserve it; populateConfig and the handler still choose BBRv3.
	c.EnableBBR = true
}

// EnableBBRv3CongestionControl normalizes legacy flags to the only supported
// policy: lightly tuned BBRv3. No call is needed for a new Config.
func (c *Config) EnableBBRv3CongestionControl() {
	c.EnableBBRv3 = true
	c.EnableBBR = false
	c.EnableCubic = false
}

// CongestionControlName reports the actual policy, including legacy Configs.
// The stable diagnostic identity is preserved for existing integrations.
func (c *Config) CongestionControlName() string { return "bbr-v3" }
