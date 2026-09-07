package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestBBRAckOnlyTrafficCannotStarveControlPacketPacing(t *testing.T) {
	clock := mockClock(time.Second)
	b := NewBBRSender(&clock, utils.NewRTTStats(), 1200)
	// The reverse direction carries only occasional congestion-controlled
	// packets, so its measured rate can be tiny while incoming bulk data
	// continually requires pure ACKs in the opposite direction.
	b.pacingRate = 1000 * BytesPerSecond
	var pn protocol.PacketNumber
	var flight protocol.ByteCount
	for range maxBurstSizePackets {
		pn++
		flight += 1200
		b.OnPacketSent(clock.Now(), flight, pn, 1200, true)
	}
	require.False(t, b.HasPacingBudget(clock.Now()), "ack-eliciting data must consume the initial burst budget")
	deadline := b.TimeUntilSend(flight)
	require.Equal(t, clock.Now().Add(1200*time.Millisecond), deadline)

	// Pure ACKs bypass the connection's pacing gate. Their 6.4 kB/s rate
	// exceeds this direction's 1 kB/s control-data pacing rate; charging them
	// would continually zero the budget and starve the credit update forever.
	for range 120 {
		clock.Advance(10 * time.Millisecond)
		pn++
		b.OnPacketSent(clock.Now(), flight, pn, 64, false)
		require.Equal(t, deadline, b.TimeUntilSend(flight), "pure ACKs must not postpone a queued control packet")
	}
	require.True(t, b.HasPacingBudget(clock.Now()), "control traffic must acquire its budget despite continuous ACKs")

	// MAX_STREAM_DATA is ACK-eliciting and must still pay the normal pacing
	// cost. Exempting pure ACKs must not disable BBR pacing for real packets.
	pn++
	flight += 1200
	b.OnPacketSent(clock.Now(), flight, pn, 1200, true)
	require.False(t, b.HasPacingBudget(clock.Now()))
	require.Equal(t, clock.Now().Add(1200*time.Millisecond), b.TimeUntilSend(flight))
}
