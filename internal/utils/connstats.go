package utils

import "sync/atomic"

// ConnectionStats stores stats for the connection. See the public
// ConnectionStats struct in connection.go for more information
type ConnectionStats struct {
	BytesSent                    atomic.Uint64
	PacketsSent                  atomic.Uint64
	BytesReceived                atomic.Uint64
	PacketsReceived              atomic.Uint64
	BytesLost                    atomic.Uint64
	PacketsLost                  atomic.Uint64
	CongestionWindow             atomic.Uint64
	BytesInFlight                atomic.Uint64
	SlowStart                    atomic.Bool
	SlowStartExits               atomic.Uint64
	ApplicationLimitedRTTSamples atomic.Uint64
}
