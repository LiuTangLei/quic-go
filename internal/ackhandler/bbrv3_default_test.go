package ackhandler

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

func TestDefaultBBRv3SurvivesMigrationAndLegacySelectors(t *testing.T) {
	for _, flags := range [][]bool{nil, {true}, {false, true}, {false, false, true}, {true, true, true}} {
		h := NewSentPacketHandler(0, 1200, utils.NewRTTStats(), &utils.ConnectionStats{}, true, true, nil, protocol.PerspectiveClient, nil, utils.DefaultLogger, flags...).(*sentPacketHandler)
		check := func(size int) {
			t.Helper()
			named, ok := h.congestion.(interface{ ControllerName() string })
			if !ok || named.ControllerName() != "bbr-v3" || !h.useBBR || !h.useBBRv3 {
				t.Fatalf("wrong production controller for flags %v", flags)
			}
			if h.ecnTracker != nil || h.enableECN {
				t.Fatal("BBRv3 must not silently consume CE without a response")
			}
			if h.congestion.GetCongestionWindow() != protocol.ByteCount(32*size) {
				t.Fatal("incorrect initial window")
			}
		}
		check(1200)
		h.MigratedPath(monotime.Now().Add(time.Second), 1280)
		check(1280)
	}
}
