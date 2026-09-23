package quic

import "testing"

func TestOnlyBBRv3ForZeroAndLegacyConfigs(t *testing.T) {
	configs := []*Config{nil, {}}
	for bits := 0; bits < 8; bits++ {
		configs = append(configs, &Config{EnableCubic: bits&1 != 0, EnableBBR: bits&2 != 0, EnableBBRv3: bits&4 != 0})
	}
	for _, config := range configs {
		if err := validateConfig(config); err != nil {
			t.Fatal(err)
		}
		got := populateConfig(config)
		if !got.EnableBBRv3 || got.EnableBBR || got.EnableCubic || got.CongestionControlName() != "bbr-v3" {
			t.Fatalf("did not normalize to BBRv3: %+v", got)
		}
		if config.CongestionControlName() != "bbr-v3" {
			t.Fatal("legacy diagnostic advertised an unavailable algorithm")
		}
	}
	for _, set := range []func(*Config){(*Config).EnableBBRCongestionControl, (*Config).EnableCubicCongestionControl, (*Config).EnableBBRv3CongestionControl} {
		c := &Config{EnableBBR: true, EnableCubic: true}
		set(c)
		got := populateConfig(c)
		if !got.EnableBBRv3 || got.EnableBBR || got.EnableCubic || c.CongestionControlName() != "bbr-v3" {
			t.Fatalf("legacy helper failed: %+v", c)
		}
	}
}
