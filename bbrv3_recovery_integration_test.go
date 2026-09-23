package quic

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// These regressions use virtual time, not Internet or native-socket speed.
// Keep the original 200 MiB payload and automatic deadline: shortening the
// transfer can hide the capacity-drop failure, and extending its timeout can
// hide the random-loss throughput collapse.
func TestBBRv3RecoveryLongTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("200 MiB virtual-link recovery regressions")
	}
	for _, name := range []string{"capacity-drop", "random-loss"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "samples.jsonl")
			cfg := bbrLongConfig{
				mode: "virtual", caseName: "loss", variant: "recovery-regression", repeat: "1",
				output: path, profile: "chromium-h3", direction: "forward", payloadBytes: 200 << 20,
				streamWindowBytes: 32 << 20, connectionWindowBytes: 64 << 20,
				rtt: 60 * time.Millisecond, sampleInterval: 250 * time.Millisecond,
				mbps: 20, reverseMbps: 20, queueBDP: 4, lossPercent: 5, seed: 97,
			}
			if name == "capacity-drop" {
				cfg.caseName, cfg.mbps, cfg.reverseMbps = "step", 100, 100
				cfg.lossPercent, cfg.seed = 0, 42
				cfg.stepAfter, cfg.stepMbps = 5*time.Second, 20
			}
			synctest.Test(t, func(t *testing.T) { runBBRLongTransfer(t, cfg) })
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()
			scanner := bufio.NewScanner(f)
			var summary map[string]any
			for scanner.Scan() {
				var record map[string]any
				require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
				if record["type"] == "summary" {
					summary = record
				}
			}
			require.NoError(t, scanner.Err())
			require.NotNil(t, summary)
			require.Equal(t, true, summary["success"])
			if name == "random-loss" {
				require.Greater(t, summary["goodput_mbps"].(float64), 8.0,
					"reliable completion alone must not conceal repeated random-loss collapse")
			}
		})
	}
}
