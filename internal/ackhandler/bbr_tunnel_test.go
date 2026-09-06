package ackhandler

import (
	"github.com/quic-go/quic-go/internal/congestion"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"testing"
	"time"
)

type samplingRecorder struct {
	congestion.SendAlgorithmWithDebugInfos
	sent, discarded []protocol.PacketNumber
}

func (r *samplingRecorder) OnPacketSent(at monotime.Time, flight protocol.ByteCount, pn protocol.PacketNumber, size protocol.ByteCount, eliciting bool) {
	r.sent = append(r.sent, pn)
	r.SendAlgorithmWithDebugInfos.OnPacketSent(at, flight, pn, size, eliciting)
}
func (r *samplingRecorder) OnPacketDiscarded(pn protocol.PacketNumber) {
	r.discarded = append(r.discarded, pn)
}

func TestBBRSamplerNumbersDistinctAcrossPacketSpaces(t *testing.T) {
	stats := new(utils.ConnectionStats)
	h := NewSentPacketHandler(0, 1250, utils.NewRTTStats(), stats, false, false, nil, protocol.PerspectiveClient, nil, utils.DefaultLogger, false, true).(*sentPacketHandler)
	recorder := &samplingRecorder{SendAlgorithmWithDebugInfos: h.congestion}
	h.congestion = recorder
	var packets packetTracker
	now := monotime.Now()
	for _, level := range []protocol.EncryptionLevel{protocol.EncryptionInitial, protocol.EncryptionHandshake, protocol.Encryption1RTT} {
		pn := h.PopPacketNumber(level)
		if pn != 0 {
			t.Fatal("fixture did not reuse wire packet number zero")
		}
		h.SentPacket(now, pn, protocol.InvalidPacketNumber, nil, []Frame{packets.NewPingFrame(pn)}, level, protocol.ECNNon, 1250, false, false)
	}
	if len(recorder.sent) != 3 || recorder.sent[0] >= recorder.sent[1] || recorder.sent[1] >= recorder.sent[2] {
		t.Fatal("sampler packet numbers collide", recorder.sent)
	}
	h.DropPackets(protocol.EncryptionInitial, now.Add(time.Millisecond))
	h.DropPackets(protocol.EncryptionHandshake, now.Add(2*time.Millisecond))
	if len(recorder.discarded) != 2 || recorder.discarded[0] != recorder.sent[0] || recorder.discarded[1] != recorder.sent[1] {
		t.Fatal("dropped keyspace leaked samples", recorder.discarded)
	}
	h.MigratedPath(now.Add(time.Second), 1200)
	if !h.useBBR || h.congestion.GetCongestionWindow() != 32*1200 || !h.congestion.InSlowStart() {
		t.Fatal("migration lost BBR policy or old MTU/window survived")
	}
}
