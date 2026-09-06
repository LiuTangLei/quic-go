package congestion

import (
	"github.com/quic-go/quic-go/internal/protocol"
	"testing"
	"time"
)

// A receiving tunnel sends a continuous stream of small, congestion-controlled
// DATAGRAMs containing inner TCP ACKs. RTT variation while only a small part of
// cwnd is used must not train HyStart into permanently exiting slow start before
// this direction gets a chance to transmit bulk data.
func TestHyStartDoesNotExitOnApplicationLimitedACKTraffic(t *testing.T) {
	s := newTestCubicSender(false)
	s.sender.congestionWindow = 32 * maxDatagramSize
	s.rttStats.UpdateRTT(60*time.Millisecond, 0)
	s.sender.OnPacketSent(s.clock.Now(), 4000, protocol.PacketNumber(100), 100, true)
	for i := 0; i < 8; i++ {
		s.rttStats.UpdateRTT(80*time.Millisecond, 0)
		s.sender.MaybeExitSlowStart(4000)
		s.sender.OnPacketAcked(protocol.PacketNumber(i), 100, 4000, s.clock.Now())
	}
	if !s.sender.InSlowStart() {
		t.Fatalf("application-limited ACK traffic exited slow start at cwnd=%d; reverse bulk would be Reno-limited", s.sender.GetCongestionWindow())
	}
}
