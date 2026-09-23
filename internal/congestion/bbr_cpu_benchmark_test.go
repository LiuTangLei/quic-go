package congestion

import (
	"fmt"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

var benchmarkBBRRate Bandwidth

// Constant-sized flights isolate packet bookkeeping. They do not measure
// packet protection, socket cost or network throughput.
func BenchmarkBBRPacketBookkeeping(b *testing.B) {
	for _, flight := range []int{32, 512} {
		b.Run(fmt.Sprintf("sampler/flight=%d", flight), func(b *testing.B) {
			s := NewBandwidthSampler()
			now := monotime.Now()
			pn := protocol.PacketNumber(0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < flight; j++ {
					s.OnPacketSent(now.Add(time.Duration(j)*time.Microsecond), pn+protocol.PacketNumber(j), 1200, protocol.ByteCount(j)*1200, true)
				}
				now = now.Add(50 * time.Millisecond)
				for j := 0; j < flight; j++ {
					v := s.OnPacketAcked(now, pn+protocol.PacketNumber(j))
					benchmarkBBRRate = v.bandwidth
				}
				pn += protocol.PacketNumber(flight)
			}
		})
		b.Run(fmt.Sprintf("v3/flight=%d", flight), func(b *testing.B) {
			clock := mockClock(monotime.Now())
			rtt := utils.NewRTTStats()
			rtt.UpdateRTT(50*time.Millisecond, 0)
			s := NewBBRv3Sender(&clock, rtt, 1200)
			pn := protocol.PacketNumber(0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < flight; j++ {
					s.OnPacketSent(clock.Now(), protocol.ByteCount(j+1)*1200, pn+protocol.PacketNumber(j), 1200, true)
				}
				clock = mockClock(clock.Now().Add(50 * time.Millisecond))
				for j := 0; j < flight; j++ {
					s.OnPacketAcked(pn+protocol.PacketNumber(j), 1200, protocol.ByteCount(flight-j)*1200, clock.Now())
				}
				pn += protocol.PacketNumber(flight)
			}
			benchmarkBBRRate = s.BandwidthEstimate()
		})
	}
}
