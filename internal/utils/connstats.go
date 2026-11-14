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
	BBRState                     atomic.Uint32 // 0 unknown; otherwise BBRv3 mode + 1
	SlowStart                    atomic.Bool
	SlowStartExits               atomic.Uint64
	ApplicationLimitedRTTSamples atomic.Uint64
}

// BBRStateName names the published BBRv3 mode without reading controller state
// from application goroutines. The controller publishes only on transitions.
func BBRStateName(state uint32) string {
	names := [...]string{"unknown", "startup", "drain", "probe_bw_down", "probe_bw_cruise", "probe_bw_refill", "probe_bw_up", "probe_rtt"}
	if state >= uint32(len(names)) {
		return names[0]
	}
	return names[state]
}
