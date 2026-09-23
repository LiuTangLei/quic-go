// Copyright (c) 2026 LiuTangLei contributors.
// SPDX-License-Identifier: BSD-3-Clause
//
// Independent QUIC adaptation of BBRv3, based on the algorithm described in
// draft-ietf-ccwg-bbr-06 (July 2026). See BBRv3.md for the specification mapping,
// transport-specific decisions, and the IETF code-component license notice.
// This controller does not embed or use the BBRv1 control law.
package congestion

import (
	"math/bits"
	"math/rand/v2"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

type bbrv3Mode uint8

const (
	bbrv3Startup bbrv3Mode = iota
	bbrv3Drain
	bbrv3ProbeBWDown
	bbrv3ProbeBWCruise
	bbrv3ProbeBWRefill
	bbrv3ProbeBWUp
	bbrv3ProbeRTT
)

type bbrv3AckPhase uint8

const (
	bbrv3AcksInit bbrv3AckPhase = iota
	bbrv3AcksRefilling
	bbrv3AcksProbeStarting
	bbrv3AcksProbeFeedback
	bbrv3AcksProbeStopping
)

const (
	bbrv3StartupGain      = 2.77
	bbrv3DrainGain        = 0.5
	bbrv3DownGain         = 0.90
	bbrv3UpGain           = 1.25
	bbrv3Beta             = 0.7
	bbrv3Headroom         = 0.10 // light tuning: retain 10% margin, not zero
	bbrv3LossThreshold    = 0.02
	bbrv3MinRTTWindow     = 10 * time.Second
	bbrv3ProbeRTTInterval = 5 * time.Second
	bbrv3ProbeRTTDuration = 200 * time.Millisecond
	bbrv3Infinity         = protocol.MaxByteCount

	// Reprobe sooner than the draft's 2–3 seconds without increasing UP
	// gain or weakening loss response. Random jitter still avoids lockstep.
	bbrv3ProbeWaitBase   = time.Second
	bbrv3ProbeWaitJitter = time.Second
)

type bbrv3SentPacket struct {
	inflight    protocol.ByteCount
	cwndLimited bool
}

type bbrv3AckBucket struct {
	round int64
	bytes protocol.ByteCount
}

type bbrv3Undo struct {
	valid                               bool
	mode                                bbrv3Mode
	bwShortterm                         Bandwidth
	inflightShortterm, inflightLongterm protocol.ByteCount
	cwnd                                protocol.ByteCount
}

// The sender is owned by the QUIC connection's run loop. Only published stats
// are atomic. Packet-number keys use ackhandler's connection-wide congestion
// sequence, not encryption-space-local QUIC packet numbers.
type bbrv3Sender struct {
	clock                                        Clock
	rttStats                                     *utils.RTTStats
	connStats                                    *utils.ConnectionStats
	liveFlightStats                              bool
	pacer                                        *pacer
	sampler                                      *BandwidthSampler
	sent                                         map[protocol.PacketNumber]bbrv3SentPacket
	lastSendPacket                               protocol.PacketNumber
	bytesInFlight                                protocol.ByteCount
	maxDatagramSize                              protocol.ByteCount
	initialCongestionWindow, maxCongestionWindow protocol.ByteCount
	congestionWindow, priorCwnd                  protocol.ByteCount

	mode                             bbrv3Mode
	ackPhase                         bbrv3AckPhase
	pacingGain, congestionWindowGain float64
	pacingRate                       Bandwidth
	sendQuantum                      protocol.ByteCount
	hasRTTSample                     bool

	// Long-term (two probe-cycle) rate model, and loss-derived bounds.
	maxBwCurrent, maxBwPrevious         Bandwidth
	bwShortterm                         Bandwidth
	inflightShortterm, inflightLongterm protocol.ByteCount
	cycleCount                          uint64

	// Packet-timed rounds use delivered-byte sent-state, not wall time.
	roundTripCount                         int64
	nextRoundDelivered                     protocol.ByteCount
	roundStart                             bool
	drainStartRound                        int64
	fullBandwidthReached, fullBandwidthNow bool
	fullBandwidth                          Bandwidth
	fullBandwidthCount                     int

	bwLatest                               Bandwidth
	inflightLatest                         protocol.ByteCount
	lossRoundDelivered                     protocol.ByteCount
	lossRoundStart, lossInRound            bool
	lossRangesInRound                      int
	lastLostPacket                         protocol.PacketNumber
	lostInRound, deliveredAtLossRoundStart protocol.ByteCount

	cycleStamp                                                monotime.Time
	probeWait                                                 time.Duration
	roundsSinceProbeUp                                        int64
	isBWProbeSample, prevProbeTooHigh, prevProbePrecautionary bool
	probeUpRounds                                             uint
	probeUpAcked, probeUpAckedPerIncrement                    protocol.ByteCount

	minRtt                             time.Duration
	minRttTimestamp                    monotime.Time
	probeRttMinDelay                   time.Duration
	probeRttMinStamp                   monotime.Time
	probeRttExpired, probeRttRoundDone bool
	probeRttDoneStamp                  monotime.Time
	idleRestart                        bool

	extraAcked          [10]bbrv3AckBucket
	extraAckedEpoch     monotime.Time
	extraAckedDelivered protocol.ByteCount

	inRecovery         bool
	recoveryEnd        protocol.PacketNumber
	recoveryStartRound int64
	undo               bbrv3Undo
}

var _ SendAlgorithmWithDebugInfos = (*bbrv3Sender)(nil)

func NewBBRv3Sender(clock Clock, rttStats *utils.RTTStats, size protocol.ByteCount, stats ...*utils.ConnectionStats) *bbrv3Sender {
	if size <= 0 {
		panic("BBRv3: invalid maximum datagram size")
	}
	now := clock.Now()
	b := &bbrv3Sender{
		clock: clock, rttStats: rttStats, sampler: NewBandwidthSampler(),
		sent:                    make(map[protocol.PacketNumber]bbrv3SentPacket),
		lastSendPacket:          protocol.InvalidPacketNumber,
		lastLostPacket:          protocol.InvalidPacketNumber,
		maxDatagramSize:         size,
		initialCongestionWindow: 32 * size,
		maxCongestionWindow:     protocol.MaxCongestionWindowPackets * size,
		congestionWindow:        32 * size,
		bwShortterm:             InfiniteBandwidth,
		inflightShortterm:       bbrv3Infinity, inflightLongterm: bbrv3Infinity,
		probeUpAckedPerIncrement: bbrv3Infinity,
		minRttTimestamp:          now, probeRttMinStamp: now,
		extraAckedEpoch: now,
		connStats:       new(utils.ConnectionStats),
	}
	if len(stats) > 0 && stats[0] != nil {
		b.connStats, b.liveFlightStats = stats[0], true
	}
	b.minRtt = rttStats.MinRTT()
	b.probeRttMinDelay = b.minRtt
	b.hasRTTSample = b.minRtt > 0
	b.setMode(bbrv3Startup)
	b.initPacingRate()
	b.pacer = newPacer(func() Bandwidth { return b.pacingRate })
	// Generic Reno/CUBIC pacing adds 25%. BBR already applies its own gains.
	b.pacer.adjustedBandwidth = func() uint64 { return max(1, uint64(b.pacingRate/BytesPerSecond)) }
	b.pacer.SetMaxDatagramSize(size)
	b.updateControl(0)
	return b
}

func (b *bbrv3Sender) ControllerName() string                  { return "bbr-v3" }
func (b *bbrv3Sender) InSlowStart() bool                       { return b.mode == bbrv3Startup }
func (b *bbrv3Sender) InRecovery() bool                        { return b.inRecovery }
func (b *bbrv3Sender) GetCongestionWindow() protocol.ByteCount { return b.congestionWindow }
func (b *bbrv3Sender) BandwidthEstimate() Bandwidth            { return max(b.maxBwPrevious, b.maxBwCurrent) }
func (b *bbrv3Sender) modelBandwidth() Bandwidth               { return min(b.BandwidthEstimate(), b.bwShortterm) }
func (b *bbrv3Sender) minimumCwnd() protocol.ByteCount         { return 4 * b.maxDatagramSize }
func (b *bbrv3Sender) GetMinRtt() time.Duration {
	if b.minRtt > 0 {
		return b.minRtt
	}
	return InitialRtt
}
func (b *bbrv3Sender) TimeUntilSend(inflight protocol.ByteCount) monotime.Time {
	b.bytesInFlight = max(0, inflight)
	return b.pacer.TimeUntilSend()
}
func (b *bbrv3Sender) HasPacingBudget(now monotime.Time) bool {
	return b.pacer.Budget(now) >= b.maxDatagramSize
}
func (b *bbrv3Sender) CanSend(inflight protocol.ByteCount) bool {
	b.bytesInFlight = max(0, inflight)
	return inflight < b.congestionWindow
}
func (b *bbrv3Sender) MaybeExitSlowStart(protocol.ByteCount) {}
func (b *bbrv3Sender) ShouldSendProbingPacket() bool         { return b.pacingGain > 1 }
func (b *bbrv3Sender) IsPipeSufficientlyFull() bool {
	gain := 1.1
	if b.mode == bbrv3Startup {
		gain = 1.5
	} else if b.pacingGain > 1 {
		gain = b.pacingGain
	}
	return b.bytesInFlight >= b.inflight(gain)
}

func (b *bbrv3Sender) OnApplicationLimited(inflight protocol.ByteCount) {
	if inflight >= b.congestionWindow {
		return
	}
	// QUIC may have drained its send queue while a full pipe is still in
	// flight. That is not evidence that the network wasn't being probed.
	if b.BandwidthEstimate() > 0 {
		gain := .9
		if b.mode == bbrv3Startup {
			gain = 1.5
		}
		if inflight >= b.inflight(gain) {
			return
		}
	}
	b.sampler.OnAppLimited()
	b.connStats.ApplicationLimitedRTTSamples.Add(1)
}

func (b *bbrv3Sender) OnPacketSent(now monotime.Time, inflight protocol.ByteCount, number protocol.PacketNumber, size protocol.ByteCount, ackEliciting bool) {
	b.lastSendPacket = max(b.lastSendPacket, number)
	b.bytesInFlight = max(0, inflight)
	priorFlight := inflight
	if ackEliciting {
		priorFlight = max(0, inflight-size)
	}
	if ackEliciting && priorFlight == 0 && b.sampler.isAppLimited {
		b.idleRestart = true
		b.extraAckedEpoch, b.extraAckedDelivered = now, 0
		if b.inProbeBW() && b.modelBandwidth() > 0 {
			b.pacingRate = bbrv3ScaleBandwidth(b.modelBandwidth(), .99)
		} else if b.mode == bbrv3ProbeRTT {
			b.checkProbeRTTDone(now)
		}
	}
	b.sampler.OnPacketSent(now, number, size, priorFlight, ackEliciting)
	if !ackEliciting {
		return
	}
	b.sent[number] = bbrv3SentPacket{
		inflight:    max(size, inflight),
		cwndLimited: inflight+b.maxDatagramSize >= b.congestionWindow,
	}
	b.pacer.SentPacket(now, size)
}

func (b *bbrv3Sender) OnPacketAcked(number protocol.PacketNumber, ackedBytes, priorInFlight protocol.ByteCount, now monotime.Time) {
	meta, ok := b.sent[number]
	if !ok {
		return
	} // never inflate delivered bytes for duplicates/discards
	delete(b.sent, number)
	// Take the original send-state once. A separate Get followed by the
	// sampler's public OnPacketAcked would look up and copy the same map
	// value twice per ACK. Missing/duplicate/discarded records remain no-ops.
	found, prior := b.sampler.connectionStats.Remove(number)
	if !found {
		return
	}
	state := prior.sendTimeState
	packetBytes := prior.size
	before := b.sampler.totalBytesAcked
	sample := b.sampler.onPacketAckedInner(now, number, &prior)
	ackedBytes = b.sampler.totalBytesAcked - before
	if ackedBytes <= 0 {
		return
	}
	b.bytesInFlight = max(0, priorInFlight-packetBytes)
	if b.liveFlightStats {
		// ackhandler updates this counter after every packet callback, whereas
		// priorInFlight is the same for the entire ACK frame.
		b.bytesInFlight = max(0, protocol.ByteCount(b.connStats.BytesInFlight.Load())-packetBytes)
	}
	endRecovery := b.inRecovery && number > b.recoveryEnd
	// Retain the recovery state through model updates: Startup's high-loss
	// exit needs the first complete recovery round, including this ACK.
	defer func() {
		if endRecovery {
			b.inRecovery = false
			b.restoreCwnd()
			b.updateControl(0)
		}
	}()

	// Invalid timestamp samples still acknowledge bytes, but cannot change
	// the path-rate model. Packet lifecycle and cwnd accounting are preserved.
	if !sample.stateAtSend.isValid {
		b.updateControl(ackedBytes)
		return
	}
	delivered := max(0, b.sampler.totalBytesAcked-state.totalBytesAcked)
	lost := max(0, b.sampler.totalBytesLost-state.totalBytesLost)
	b.roundStart = state.totalBytesAcked >= b.nextRoundDelivered
	if b.roundStart {
		b.startRound()
		b.roundTripCount++
		b.roundsSinceProbeUp++
	}
	b.bwLatest = max(b.bwLatest, sample.bandwidth)
	b.inflightLatest = max(b.inflightLatest, delivered)
	b.lossRoundStart = state.totalBytesAcked >= b.lossRoundDelivered
	if b.lossRoundStart {
		b.lossRoundDelivered = b.sampler.totalBytesAcked
	}
	if sample.bandwidth > 0 && (!state.isAppLimited || sample.bandwidth >= b.BandwidthEstimate()) {
		b.maxBwCurrent = max(b.maxBwCurrent, sample.bandwidth)
	}
	if b.lossRoundStart {
		b.adaptShorttermModel()
		b.checkStartupLoss()
		b.lossInRound = false
		b.lostInRound, b.lossRangesInRound = 0, 0
		b.lastLostPacket = protocol.InvalidPacketNumber
		b.deliveredAtLossRoundStart = b.sampler.totalBytesAcked
	}
	b.updateAckAggregation(now, ackedBytes)
	b.checkFullBandwidth(sample.bandwidth, state.isAppLimited)
	if b.mode == bbrv3Startup && b.fullBandwidthReached {
		b.enterDrain()
	}
	if b.mode == bbrv3Drain && (b.bytesInFlight <= b.inflight(1) || b.roundTripCount > b.drainStartRound+3) {
		b.startProbeDown(now)
	}
	b.updateProbeBW(now, sample.bandwidth, meta, state.isAppLimited, lost, ackedBytes)
	b.updateMinRTT(now, sample.rtt)
	b.checkProbeRTT(now, delivered)
	if b.lossRoundStart {
		b.bwLatest, b.inflightLatest = sample.bandwidth, delivered
	}
	b.updateControl(ackedBytes)
}

func (b *bbrv3Sender) OnCongestionEvent(number protocol.PacketNumber, lostBytes, priorInFlight protocol.ByteCount) {
	// BBRv3 in draft-06 does not consume ECN. Its caller disables ECN
	// validation for this controller rather than pretending to react to CE.
	if lostBytes <= 0 {
		return
	}
	meta, ok := b.sent[number]
	if !ok {
		return
	}
	prior, found := b.sampler.connectionStats.Get(number)
	if !found {
		delete(b.sent, number)
		return
	}
	size := prior.size
	state := b.sampler.OnPacketLost(number)
	delete(b.sent, number)
	if !state.isValid {
		return
	}
	b.connStats.PacketsLost.Add(1)
	b.connStats.BytesLost.Add(uint64(size))
	b.bytesInFlight = max(0, priorInFlight-size)
	if b.liveFlightStats {
		b.bytesInFlight = protocol.ByteCount(b.connStats.BytesInFlight.Load())
	}
	if !b.inRecovery {
		b.saveCwnd()
		b.saveUndo()
		b.inRecovery, b.recoveryEnd = true, b.lastSendPacket
		b.recoveryStartRound = b.roundTripCount
	}
	if !b.lossInRound {
		b.lossRoundDelivered = b.sampler.totalBytesAcked
		b.saveUndo()
	}
	b.lossInRound = true
	b.lostInRound = bbrv3AddBytes(b.lostInRound, size)
	if b.lastLostPacket == protocol.InvalidPacketNumber || number != b.lastLostPacket+1 {
		b.lossRangesInRound++
	}
	b.lastLostPacket = number
	lost := max(0, b.sampler.totalBytesLost-state.totalBytesLost)
	if b.isBWProbeSample && b.inflightTooHigh(lost, meta.inflight) {
		// Estimate the loss-threshold crossing inside the packet, using its
		// send-time flight, not the much smaller flight at late loss detection.
		previousFlight := max(0, meta.inflight-size)
		previousLost := max(0, lost-size)
		threshold := b.lossThreshold()
		prefix := protocol.ByteCount((threshold*float64(previousFlight) - float64(previousLost)) / (1 - threshold))
		atLoss := previousFlight + min(size, max(0, prefix))
		b.handleInflightTooHigh(b.clock.Now(), atLoss, state.isAppLimited)
	}
	b.updateStats()
}

func (b *bbrv3Sender) OnPacketDiscarded(number protocol.PacketNumber) {
	delete(b.sent, number)
	b.sampler.connectionStats.Remove(number)
}

func (b *bbrv3Sender) SetMaxDatagramSize(size protocol.ByteCount) {
	if size < b.maxDatagramSize {
		panic("BBRv3: decreased packet size without path reset")
	}
	b.maxDatagramSize = size
	b.initialCongestionWindow = 32 * size
	b.maxCongestionWindow = protocol.MaxCongestionWindowPackets * size
	b.congestionWindow = max(b.minimumCwnd(), min(b.congestionWindow, b.maxCongestionWindow))
	b.pacer.SetMaxDatagramSize(size)
	b.updateStats()
}

func (b *bbrv3Sender) OnRetransmissionTimeout(retransmitted bool) {
	if !retransmitted {
		return
	}
	b.saveCwnd()
	b.saveUndo()
	b.inRecovery, b.recoveryEnd = true, b.lastSendPacket
	b.recoveryStartRound = b.roundTripCount
	b.congestionWindow = min(b.maxCongestionWindow, b.bytesInFlight+b.maxDatagramSize)
	b.updateStats()
}

// OnSpuriousLossRecovery is available to transports that can prove an entire
// recovery episode spurious. A single late ACK is NOT sufficient proof.
func (b *bbrv3Sender) OnSpuriousLossRecovery() {
	if !b.undo.valid {
		return
	}
	u := b.undo
	b.inRecovery, b.lossInRound = false, false
	b.bwShortterm = max(b.bwShortterm, u.bwShortterm)
	b.inflightShortterm = max(b.inflightShortterm, u.inflightShortterm)
	b.inflightLongterm = max(b.inflightLongterm, u.inflightLongterm)
	b.congestionWindow = max(b.congestionWindow, u.cwnd)
	b.resetFullBandwidth()
	if u.mode == bbrv3Startup {
		b.fullBandwidthReached = false
		if b.mode != bbrv3ProbeRTT {
			b.setMode(bbrv3Startup)
		}
	} else if u.mode == bbrv3ProbeBWUp && b.mode != bbrv3ProbeRTT {
		b.startProbeRefill()
	}
	b.undo.valid = false
	b.updateControl(0)
}

func (b *bbrv3Sender) setMode(mode bbrv3Mode) {
	if b.mode == bbrv3Startup && mode != bbrv3Startup {
		b.connStats.SlowStartExits.Add(1)
	}
	b.mode, b.congestionWindowGain = mode, 2
	switch mode {
	case bbrv3Startup:
		b.pacingGain = bbrv3StartupGain
	case bbrv3Drain:
		b.pacingGain = bbrv3DrainGain
	case bbrv3ProbeBWDown:
		b.pacingGain = bbrv3DownGain
	case bbrv3ProbeBWCruise, bbrv3ProbeBWRefill:
		b.pacingGain = 1
	case bbrv3ProbeBWUp:
		b.pacingGain, b.congestionWindowGain = bbrv3UpGain, 2.25
	case bbrv3ProbeRTT:
		b.pacingGain, b.congestionWindowGain = 1, .5
	}
}
func (b *bbrv3Sender) inProbeBW() bool { return b.mode >= bbrv3ProbeBWDown && b.mode <= bbrv3ProbeBWUp }
func (b *bbrv3Sender) probingBandwidth() bool {
	return b.mode == bbrv3Startup || b.mode == bbrv3ProbeBWRefill || b.mode == bbrv3ProbeBWUp
}
func (b *bbrv3Sender) startRound() { b.nextRoundDelivered = b.sampler.totalBytesAcked }
func (b *bbrv3Sender) resetFullBandwidth() {
	b.fullBandwidth, b.fullBandwidthCount, b.fullBandwidthNow = 0, 0, false
}
func (b *bbrv3Sender) checkFullBandwidth(rate Bandwidth, appLimited bool) {
	if b.fullBandwidthNow || !b.roundStart || appLimited || rate == 0 {
		return
	}
	if rate >= bbrv3ScaleBandwidth(b.fullBandwidth, 1.25) {
		b.resetFullBandwidth()
		b.fullBandwidth = rate
		return
	}
	b.fullBandwidthCount++
	if b.fullBandwidthCount >= 3 {
		b.fullBandwidthNow, b.fullBandwidthReached = true, true
	}
}
func (b *bbrv3Sender) checkStartupLoss() {
	if b.mode != bbrv3Startup || !b.inRecovery || b.roundTripCount <= b.recoveryStartRound || b.lossRangesInRound < 6 {
		return
	}
	delivered := max(0, b.sampler.totalBytesAcked-b.deliveredAtLossRoundStart)
	if !b.inflightTooHigh(b.lostInRound, delivered+b.lostInRound) {
		return
	}
	b.fullBandwidthReached = true
	b.inflightLongterm = max(b.bdp(1), b.inflightLatest, b.minimumCwnd())
}
func (b *bbrv3Sender) enterDrain() {
	b.setMode(bbrv3Drain)
	b.drainStartRound = b.roundTripCount
}
func (b *bbrv3Sender) resetCongestionSignals() {
	b.lossInRound = false
	b.bwLatest, b.inflightLatest, b.lostInRound, b.lossRangesInRound = 0, 0, 0, 0
	b.lastLostPacket = protocol.InvalidPacketNumber
	b.deliveredAtLossRoundStart = b.sampler.totalBytesAcked
}
func (b *bbrv3Sender) resetShorttermModel() {
	b.bwShortterm, b.inflightShortterm = InfiniteBandwidth, bbrv3Infinity
}
func (b *bbrv3Sender) startProbeDown(now monotime.Time) {
	b.resetCongestionSignals()
	b.probeUpAckedPerIncrement = bbrv3Infinity
	b.roundsSinceProbeUp = int64(rand.IntN(2))
	b.probeWait = bbrv3ProbeWaitBase + time.Duration(rand.Int64N(int64(bbrv3ProbeWaitJitter)))
	b.cycleStamp = now
	b.ackPhase = bbrv3AcksProbeStopping
	b.startRound()
	b.setMode(bbrv3ProbeBWDown)
}
func (b *bbrv3Sender) startProbeRefill() {
	b.resetShorttermModel()
	b.probeUpRounds, b.probeUpAcked = 0, 0
	b.prevProbePrecautionary = false
	b.ackPhase = bbrv3AcksRefilling
	b.startRound()
	b.setMode(bbrv3ProbeBWRefill)
}
func (b *bbrv3Sender) startProbeUp(rate Bandwidth) {
	b.ackPhase = bbrv3AcksProbeStarting
	b.startRound()
	b.resetFullBandwidth()
	b.fullBandwidth = rate
	b.setMode(bbrv3ProbeBWUp)
	b.raiseInflightSlope()
}
func (b *bbrv3Sender) advanceBandwidthFilter() {
	// Do not age out a useful model when the whole cycle was app-limited.
	if b.maxBwCurrent == 0 {
		return
	}
	b.maxBwPrevious, b.maxBwCurrent = b.maxBwCurrent, 0
	b.cycleCount++
}
func (b *bbrv3Sender) timeToProbe(now monotime.Time) bool {
	rounds := max(int64(1), min(int64(63), int64(min(b.bdp(1), b.congestionWindow)/b.maxDatagramSize)))
	return now.Sub(b.cycleStamp) >= b.probeWait || b.roundsSinceProbeUp >= rounds
}
func (b *bbrv3Sender) inflightWithHeadroom() protocol.ByteCount {
	if b.inflightLongterm == bbrv3Infinity {
		return bbrv3Infinity
	}
	headroom := max(b.maxDatagramSize, bbrv3ScaleBytes(b.inflightLongterm, bbrv3Headroom))
	return max(b.minimumCwnd(), b.inflightLongterm-headroom)
}
func (b *bbrv3Sender) raiseInflightSlope() {
	growth := uint64(1) << min(b.probeUpRounds, 30)
	b.probeUpRounds = min(b.probeUpRounds+1, 30)
	b.probeUpAckedPerIncrement = max(b.maxDatagramSize, b.congestionWindow/protocol.ByteCount(growth))
}
func (b *bbrv3Sender) inflightTooHigh(lost, inflight protocol.ByteCount) bool {
	return lost > 0 && inflight > 0 && float64(lost) > b.lossThreshold()*float64(inflight)
}

func (b *bbrv3Sender) lossThreshold() float64 {
	// Established paths with no standing queue get extra random-loss
	// tolerance. Startup and inflated RTT retain the base 2% response;
	// short-term bounds still react to every loss round.
	if b.fullBandwidthReached && b.hasRTTSample && b.minRtt > 0 {
		rtt := max(b.rttStats.LatestRTT(), b.rttStats.SmoothedRTT())
		if rtt > 0 && rtt <= b.minRtt+max(time.Millisecond, b.minRtt/8) {
			return .08
		}
	}
	return bbrv3LossThreshold
}
func (b *bbrv3Sender) handleInflightTooHigh(now monotime.Time, flight protocol.ByteCount, appLimited bool) {
	b.prevProbeTooHigh, b.isBWProbeSample = true, false
	if !appLimited {
		target := min(b.bdp(1), b.congestionWindow)
		b.inflightLongterm = max(b.minimumCwnd(), flight, bbrv3ScaleBytes(target, bbrv3Beta))
	}
	// Application-limited loss must still stop UP; it only prevents lowering
	// the long-term cap from an unrepresentative flight measurement.
	if b.mode == bbrv3ProbeBWUp {
		b.startProbeDown(now)
	}
}
func (b *bbrv3Sender) updateProbeBW(now monotime.Time, rate Bandwidth, meta bbrv3SentPacket, appLimited bool, lost, acked protocol.ByteCount) {
	if !b.fullBandwidthReached {
		return
	}
	if b.ackPhase == bbrv3AcksProbeStarting && b.roundStart {
		b.ackPhase = bbrv3AcksProbeFeedback
	}
	if b.ackPhase == bbrv3AcksProbeStopping && b.roundStart {
		b.isBWProbeSample, b.ackPhase = false, bbrv3AcksInit
		if b.inProbeBW() && !appLimited {
			b.advanceBandwidthFilter()
		}
		if b.inProbeBW() && b.prevProbePrecautionary && !b.prevProbeTooHigh {
			b.startProbeRefill()
			return
		}
	}
	if b.inflightTooHigh(lost, meta.inflight) {
		if b.isBWProbeSample {
			b.handleInflightTooHigh(now, meta.inflight, appLimited)
		}
	} else if b.inflightLongterm != bbrv3Infinity {
		b.inflightLongterm = max(b.inflightLongterm, meta.inflight)
		if b.mode == bbrv3ProbeBWUp && meta.cwndLimited && b.congestionWindow >= b.inflightLongterm {
			b.probeUpAcked = bbrv3AddBytes(b.probeUpAcked, acked)
			if b.probeUpAckedPerIncrement > 0 && b.probeUpAckedPerIncrement != bbrv3Infinity {
				delta := b.probeUpAcked / b.probeUpAckedPerIncrement
				b.probeUpAcked -= delta * b.probeUpAckedPerIncrement
				b.inflightLongterm = bbrv3AddBytes(b.inflightLongterm, bbrv3ScaleBytes(delta, float64(b.maxDatagramSize)))
			}
			if b.roundStart {
				b.raiseInflightSlope()
			}
		}
	}
	switch b.mode {
	case bbrv3ProbeBWDown:
		if b.timeToProbe(now) {
			b.startProbeRefill()
			return
		}
		if b.bytesInFlight <= b.inflightWithHeadroom() && b.bytesInFlight <= b.inflight(1) {
			b.setMode(bbrv3ProbeBWCruise)
		}
	case bbrv3ProbeBWCruise:
		if b.timeToProbe(now) {
			b.startProbeRefill()
		}
	case bbrv3ProbeBWRefill:
		if b.roundStart {
			b.isBWProbeSample = true
			b.startProbeUp(rate)
		}
	case bbrv3ProbeBWUp:
		if b.prevProbeTooHigh && b.bytesInFlight >= b.inflightLongterm {
			b.prevProbePrecautionary = true
			b.prevProbeTooHigh = false
			b.startProbeDown(now)
		} else if meta.cwndLimited && b.congestionWindow >= b.inflightLongterm {
			b.resetFullBandwidth()
			b.fullBandwidth = rate
		} else if b.fullBandwidthNow {
			b.prevProbeTooHigh = false
			b.startProbeDown(now)
		}
	}
}

func (b *bbrv3Sender) adaptShorttermModel() {
	if b.probingBandwidth() || !b.lossInRound {
		return
	}
	if b.bwShortterm == InfiniteBandwidth {
		b.bwShortterm = b.BandwidthEstimate()
	}
	if b.inflightShortterm == bbrv3Infinity {
		b.inflightShortterm = b.congestionWindow
	}
	b.bwShortterm = max(b.bwLatest, bbrv3ScaleBandwidth(b.bwShortterm, bbrv3Beta))
	b.inflightShortterm = max(b.inflightLatest, bbrv3MulRatioBytes(b.inflightShortterm, 7, 10))
}
func (b *bbrv3Sender) updateMinRTT(now monotime.Time, rtt time.Duration) {
	b.probeRttExpired = now.Sub(b.probeRttMinStamp) > bbrv3ProbeRTTInterval
	if rtt <= 0 {
		return
	}
	if !b.hasRTTSample {
		b.minRtt, b.probeRttMinDelay = rtt, rtt
		b.minRttTimestamp, b.probeRttMinStamp = now, now
		b.hasRTTSample, b.probeRttExpired = true, false
		// Replace the bootstrap nominal rate with the first measured RTT.
		b.initPacingRate()
		return
	}
	if b.probeRttMinDelay == 0 || rtt < b.probeRttMinDelay || b.probeRttExpired {
		b.probeRttMinDelay, b.probeRttMinStamp = rtt, now
	}
	if b.minRtt == 0 || b.probeRttMinDelay < b.minRtt || now.Sub(b.minRttTimestamp) > bbrv3MinRTTWindow {
		b.minRtt, b.minRttTimestamp = b.probeRttMinDelay, b.probeRttMinStamp
	}
}
func (b *bbrv3Sender) ProbeRttCongestionWindow() protocol.ByteCount {
	return max(b.minimumCwnd(), b.bdp(.5))
}
func (b *bbrv3Sender) checkProbeRTT(now monotime.Time, delivered protocol.ByteCount) {
	if b.mode != bbrv3ProbeRTT && b.probeRttExpired && !b.idleRestart {
		b.saveCwnd()
		b.setMode(bbrv3ProbeRTT)
		b.probeRttDoneStamp, b.probeRttRoundDone = 0, false
		b.ackPhase = bbrv3AcksProbeStopping
		b.startRound()
	}
	if b.mode == bbrv3ProbeRTT {
		b.sampler.OnAppLimited()
		if b.probeRttDoneStamp.IsZero() && b.bytesInFlight <= b.ProbeRttCongestionWindow() {
			b.probeRttDoneStamp = now.Add(bbrv3ProbeRTTDuration)
			b.probeRttRoundDone = false
			b.startRound()
		} else if !b.probeRttDoneStamp.IsZero() {
			if b.roundStart {
				b.probeRttRoundDone = true
			}
			if b.probeRttRoundDone {
				b.checkProbeRTTDone(now)
			}
		}
	}
	if delivered > 0 {
		b.idleRestart = false
	}
}
func (b *bbrv3Sender) checkProbeRTTDone(now monotime.Time) {
	if b.probeRttDoneStamp.IsZero() || now.Before(b.probeRttDoneStamp) {
		return
	}
	b.probeRttMinStamp = now
	b.probeRttExpired = false
	b.restoreCwnd()
	b.resetShorttermModel()
	if b.fullBandwidthReached {
		b.startProbeDown(now)
		b.setMode(bbrv3ProbeBWCruise)
	} else {
		b.setMode(bbrv3Startup)
	}
}

func (b *bbrv3Sender) updateAckAggregation(now monotime.Time, acked protocol.ByteCount) {
	expected := bbrv3Volume(b.modelBandwidth(), max(time.Duration(0), now.Sub(b.extraAckedEpoch)))
	if b.extraAckedDelivered <= expected || now.Sub(b.extraAckedEpoch) > time.Second {
		b.extraAckedDelivered, b.extraAckedEpoch, expected = 0, now, 0
	}
	b.extraAckedDelivered = bbrv3AddBytes(b.extraAckedDelivered, acked)
	extra := min(b.congestionWindow, max(0, b.extraAckedDelivered-expected))
	idx := b.roundTripCount % int64(len(b.extraAcked))
	bucket := &b.extraAcked[idx]
	if bucket.round != b.roundTripCount {
		*bucket = bbrv3AckBucket{round: b.roundTripCount}
	}
	bucket.bytes = max(bucket.bytes, extra)
}
func (b *bbrv3Sender) ackAggregationAllowance() protocol.ByteCount {
	window := int64(10)
	if !b.fullBandwidthReached {
		window = 1
	}
	var extra protocol.ByteCount
	for _, bucket := range b.extraAcked {
		if bucket.round <= b.roundTripCount && b.roundTripCount-bucket.round < window {
			extra = max(extra, bucket.bytes)
		}
	}
	return extra
}
func (b *bbrv3Sender) bdp(gain float64) protocol.ByteCount {
	if !b.hasRTTSample || b.modelBandwidth() == 0 {
		return b.initialCongestionWindow
	}
	return bbrv3ScaleBytes(bbrv3Volume(b.modelBandwidth(), b.minRtt), gain)
}
func (b *bbrv3Sender) quantizationBudget(flight protocol.ByteCount) protocol.ByteCount {
	// Userspace QUIC: one sending and one receiving quantum suffice. The
	// generic pacer can burst ten datagrams at its timer granularity, so keep
	// the cwnd budget honest about the actual host batching behavior.
	flight = max(flight, 2*b.sendQuantum, b.minimumCwnd())
	if b.mode == bbrv3ProbeBWUp {
		flight = bbrv3AddBytes(flight, 2*b.maxDatagramSize)
	}
	return min(b.maxCongestionWindow, flight)
}
func (b *bbrv3Sender) inflight(gain float64) protocol.ByteCount {
	return b.quantizationBudget(b.bdp(gain))
}
func (b *bbrv3Sender) initPacingRate() {
	rtt := b.rttStats.SmoothedRTT()
	if rtt <= 0 {
		rtt = b.minRtt
	}
	if rtt <= 0 {
		rtt = time.Millisecond
	}
	b.pacingRate = bbrv3ScaleBandwidth(bbrv3Rate(b.initialCongestionWindow, rtt), bbrv3StartupGain)
}
func (b *bbrv3Sender) updateControl(acked protocol.ByteCount) {
	bw := b.modelBandwidth()
	if bw > 0 {
		rate := bbrv3ScaleBandwidth(bw, .99*b.pacingGain)
		if b.fullBandwidthReached || rate > b.pacingRate || b.mode == bbrv3ProbeRTT {
			b.pacingRate = max(BytesPerSecond, rate)
		}
	}
	b.sendQuantum = min(protocol.ByteCount(64<<10), max(2*b.maxDatagramSize, bbrv3Volume(b.pacingRate, time.Millisecond)))
	target := b.quantizationBudget(bbrv3AddBytes(b.bdp(b.congestionWindowGain), b.ackAggregationAllowance()))
	if b.fullBandwidthReached {
		b.congestionWindow = min(bbrv3AddBytes(b.congestionWindow, acked), target)
	} else if b.congestionWindow < target || b.sampler.totalBytesAcked < b.initialCongestionWindow {
		b.congestionWindow = bbrv3AddBytes(b.congestionWindow, acked)
	}
	b.congestionWindow = max(b.minimumCwnd(), min(b.congestionWindow, b.maxCongestionWindow))
	if b.mode == bbrv3ProbeRTT {
		b.congestionWindow = min(b.congestionWindow, b.ProbeRttCongestionWindow())
	}
	cap := bbrv3Infinity
	if (b.inProbeBW() && b.mode != bbrv3ProbeBWCruise) || b.mode == bbrv3Drain {
		cap = b.inflightLongterm
	}
	if b.mode == bbrv3ProbeRTT || b.mode == bbrv3ProbeBWCruise {
		cap = b.inflightWithHeadroom()
	}
	cap = max(b.minimumCwnd(), min(cap, b.inflightShortterm))
	b.congestionWindow = min(b.congestionWindow, cap)
	b.updateStats()
}
func (b *bbrv3Sender) saveCwnd() {
	if !b.inRecovery && b.mode != bbrv3ProbeRTT {
		b.priorCwnd = b.congestionWindow
	} else {
		b.priorCwnd = max(b.priorCwnd, b.congestionWindow)
	}
}
func (b *bbrv3Sender) restoreCwnd() {
	b.congestionWindow = min(b.maxCongestionWindow, max(b.congestionWindow, b.priorCwnd))
}
func (b *bbrv3Sender) saveUndo() {
	if b.undo.valid && b.inRecovery {
		return
	}
	b.undo = bbrv3Undo{true, b.mode, b.bwShortterm, b.inflightShortterm, b.inflightLongterm, b.congestionWindow}
}
func (b *bbrv3Sender) updateStats() {
	b.connStats.CongestionWindow.Store(uint64(max(0, b.congestionWindow)))
	b.connStats.SlowStart.Store(b.mode == bbrv3Startup)
}

// Arithmetic saturates rather than wrapping a model estimate or packet budget.
func bbrv3MulRatioBytes(n protocol.ByteCount, numerator, denominator uint64) protocol.ByteCount {
	if n <= 0 || numerator == 0 || denominator == 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(n), numerator)
	if hi >= denominator {
		return bbrv3Infinity
	}
	q, _ := bits.Div64(hi, lo, denominator)
	return protocol.ByteCount(min(uint64(bbrv3Infinity), q))
}
func bbrv3AddBytes(a, c protocol.ByteCount) protocol.ByteCount {
	if c > bbrv3Infinity-a {
		return bbrv3Infinity
	}
	return a + c
}
func bbrv3ScaleBytes(n protocol.ByteCount, factor float64) protocol.ByteCount {
	if n <= 0 || factor <= 0 {
		return 0
	}
	v := float64(n) * factor
	if v >= float64(bbrv3Infinity) {
		return bbrv3Infinity
	}
	return protocol.ByteCount(v)
}
func bbrv3ScaleBandwidth(rate Bandwidth, factor float64) Bandwidth {
	if rate == 0 || factor <= 0 {
		return 0
	}
	v := float64(rate) * factor
	if v >= float64(InfiniteBandwidth) {
		return InfiniteBandwidth
	}
	return Bandwidth(v)
}
func bbrv3Rate(n protocol.ByteCount, d time.Duration) Bandwidth {
	if n <= 0 || d <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(n), uint64(8*time.Second))
	if hi >= uint64(d) {
		return InfiniteBandwidth
	}
	q, _ := bits.Div64(hi, lo, uint64(d))
	return Bandwidth(q)
}
func bbrv3Volume(rate Bandwidth, d time.Duration) protocol.ByteCount {
	if rate == 0 || d <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(rate), uint64(d))
	div := uint64(8 * time.Second)
	if hi >= div {
		return bbrv3Infinity
	}
	q, _ := bits.Div64(hi, lo, div)
	return protocol.ByteCount(min(uint64(bbrv3Infinity), q))
}
