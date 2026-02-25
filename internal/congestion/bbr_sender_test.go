package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"

	"github.com/stretchr/testify/require"
)

func TestBBRSenderInterfaces(t *testing.T) {
	// Verify interface compliance
	var _ SendAlgorithm = &bbrSender{}
	var _ SendAlgorithmWithDebugInfos = &bbrSender{}
}

func TestBBRSenderInitialization(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	require.NotNil(t, bbr)
	require.True(t, bbr.InSlowStart())
	require.Equal(t, initialMaxDatagramSize, bbr.maxDatagramSize)
	require.NotNil(t, bbr.sampler)
	require.NotNil(t, bbr.pacer)
	require.NotNil(t, bbr.maxBandwidth)
	require.NotNil(t, bbr.maxAckHeight)
}

func TestBBRStartupPhase(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	require.True(t, bbr.InSlowStart())
	require.False(t, bbr.InRecovery())
	require.Equal(t, DefaultHighGain, bbr.pacingGain)
}

func TestBBRDebugMethods(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Test debug interface methods
	require.True(t, bbr.InSlowStart())
	require.False(t, bbr.InRecovery())

	cwnd := bbr.GetCongestionWindow()
	require.Greater(t, cwnd, protocol.ByteCount(0))
	require.Equal(t, bbr.congestionWindow, cwnd)
}

func TestBBRPacketSendAndAck(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Send a packet
	packetNumber := protocol.PacketNumber(1)
	packetSize := protocol.ByteCount(1000)
	bytesInFlight := protocol.ByteCount(0)

	bbr.OnPacketSent((&clock).Now(), bytesInFlight, packetNumber, packetSize, true)
	require.Equal(t, packetNumber, bbr.lastSendPacket)

	// Advance time and ack the packet
	(&clock).Advance(50 * time.Millisecond)
	bytesInFlight = packetSize

	bbr.OnPacketAcked(packetNumber, packetSize, bytesInFlight, (&clock).Now())

	// Verify bandwidth estimate exists (may be zero if no valid sample)
	_ = bbr.BandwidthEstimate()
}

func TestBBRPacketLoss(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Send some packets
	for i := protocol.PacketNumber(1); i <= 10; i++ {
		bbr.OnPacketSent((&clock).Now(), protocol.ByteCount(i-1)*1000, i, 1000, true)
	}

	// Report a loss
	lostPacket := protocol.PacketNumber(5)
	lostBytes := protocol.ByteCount(1000)
	priorInFlight := protocol.ByteCount(10000)

	bbr.OnCongestionEvent(lostPacket, lostBytes, priorInFlight)

	// In STARTUP, we might not enter recovery immediately
	// Just verify the method doesn't panic
}

func TestBBRCanSend(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// With no bytes in flight, should be able to send
	require.True(t, bbr.CanSend(0))

	// With bytes in flight below CWND, should be able to send
	require.True(t, bbr.CanSend(bbr.GetCongestionWindow()-1))

	// With bytes in flight at or above CWND, should not be able to send
	require.False(t, bbr.CanSend(bbr.GetCongestionWindow()))
	require.False(t, bbr.CanSend(bbr.GetCongestionWindow()+1000))
}

func TestBBRSetMaxDatagramSize(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	newSize := protocol.ByteCount(1400)
	bbr.SetMaxDatagramSize(newSize)

	require.Equal(t, newSize, bbr.maxDatagramSize)
}

func TestBBRTimeUntilSend(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// TimeUntilSend should return a monotime value
	timeUntilSend := bbr.TimeUntilSend(0)
	require.GreaterOrEqual(t, timeUntilSend, monotime.Time(0))
}

func TestBBRHasPacingBudget(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Initially should have pacing budget
	hasBudget := bbr.HasPacingBudget((&clock).Now())
	// Budget status depends on pacer state, just verify it doesn't panic
	_ = hasBudget
}

func TestBBRModeTransitions(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Start in STARTUP
	require.True(t, bbr.InSlowStart())

	// Force full bandwidth detection
	bbr.isAtFullBandwidth = true
	bbr.MaybeExitStartupOrDrain((&clock).Now())

	// Should transition to DRAIN
	require.False(t, bbr.InSlowStart())

	// With no bytes in flight, should transition to PROBE_BW
	bbr.bytesInFlight = 0
	bbr.MaybeExitStartupOrDrain((&clock).Now())

	// Should be in PROBE_BW now (not in slow start, not in recovery)
	require.False(t, bbr.InSlowStart())
}

func TestBBRRecoveryState(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// Send some packets first
	bbr.lastSendPacket = protocol.PacketNumber(10)

	// Initially not in recovery
	require.False(t, bbr.InRecovery())

	// Trigger recovery by reporting loss
	bbr.UpdateRecoveryState(protocol.PacketNumber(1), true, false)

	// Should enter recovery (CONSERVATION)
	require.True(t, bbr.InRecovery())

	// Advance to next round with no losses
	// This should transition to GROWTH but stay in recovery
	// because lastAckedPacket (2) < endRecoveryAt (10)
	bbr.UpdateRecoveryState(protocol.PacketNumber(2), false, true)

	// Should still be in recovery (GROWTH)
	require.True(t, bbr.InRecovery())

	// Exit recovery when we ack past endRecoveryAt
	bbr.UpdateRecoveryState(protocol.PacketNumber(11), false, false)

	// Should have exited recovery
	require.False(t, bbr.InRecovery())
}

func TestBBRGetTargetCongestionWindow(t *testing.T) {
	var clock mockClock
	rttStats := utils.NewRTTStats()
	initialMaxDatagramSize := protocol.ByteCount(1200)

	bbr := NewBBRSender(&clock, rttStats, initialMaxDatagramSize)

	// With no bandwidth estimate, should use initial congestion window
	targetCwnd := bbr.GetTargetCongestionWindow(1.0)
	require.GreaterOrEqual(t, targetCwnd, bbr.minCongestionWindow)

	// With a bandwidth estimate, should calculate BDP
	bbr.maxBandwidth.Update(1000000, 0) // 1 Mbps in bits/s
	bbr.minRtt = 100 * time.Millisecond

	targetCwnd = bbr.GetTargetCongestionWindow(1.0)
	require.Greater(t, targetCwnd, protocol.ByteCount(0))
	require.GreaterOrEqual(t, targetCwnd, bbr.minCongestionWindow)
}
