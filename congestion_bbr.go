package quic

import (
	"github.com/quic-go/quic-go/internal/congestion"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

// NewBBRCongestionController returns a factory function that creates BBR congestion controllers.
// BBR (Bottleneck Bandwidth and RTT) is a congestion control algorithm that achieves higher
// bandwidth and lower latency than loss-based algorithms like CUBIC, especially in high
// bandwidth-delay product networks.
//
// Use this with Config.Congestion to enable BBR:
//
//	config := &quic.Config{
//	    Congestion: quic.NewBBRCongestionController(),
//	}
func NewBBRCongestionController() func() CongestionController {
	return func() CongestionController {
		return congestion.NewBBRSender(
			congestion.DefaultClock{},
			utils.NewRTTStats(),
			protocol.ByteCount(protocol.InitialPacketSize),
		)
	}
}
