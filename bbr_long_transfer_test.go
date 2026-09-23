package quic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go/internal/wire"
	"github.com/quic-go/quic-go/testutils/simnet"
	"github.com/stretchr/testify/require"
)

// This opt-in experiment uses the same network model in virtual and wall-clock
// modes. It measures QUIC over a simulated bottleneck, not host UDP performance.
//
// BBR_LONG_TEST=1 BBR_LONG_MODE=virtual BBR_LONG_CASE=deep \
// BBR_LONG_VARIANT=current BBR_LONG_OUTPUT=/tmp/bbr-current.jsonl \
// go test . -run '^TestBBRLongTransfer$' -count=1 -timeout=10m -v
//
// Cases: deep (4 BDP queue), short (0.25 BDP), loss (deep + seeded 1%
// application-packet loss), step (deep + forward bandwidth 20 -> 100 Mbps at
// 20s). Queue capacity stays fixed during a bandwidth step. Optional overrides:
// BBR_LONG_MIB, BBR_LONG_MBPS, BBR_LONG_REVERSE_MBPS, BBR_LONG_RTT_MS,
// BBR_LONG_QUEUE_BDP, BBR_LONG_LOSS_PERCENT, BBR_LONG_STEP_SECONDS,
// BBR_LONG_STEP_MBPS, BBR_LONG_SAMPLE_MS, BBR_LONG_PROFILE,
// BBR_LONG_DIRECTION=forward|reverse, BBR_LONG_REPEAT, BBR_LONG_SEED,
// BBR_LONG_TIMEOUT_SECONDS (otherwise six times ideal transfer time + 30s).
func TestBBRLongTransfer(t *testing.T) {
	if os.Getenv("BBR_LONG_TEST") != "1" {
		t.Skip("set BBR_LONG_TEST=1 to run the bounded long-transfer experiment")
	}
	cfg := bbrLongConfigFromEnv(t)
	run := func(t *testing.T) { runBBRLongTransfer(t, cfg) }
	if cfg.mode == "virtual" {
		synctest.Test(t, run)
	} else {
		run(t)
	}
}

type bbrLongConfig struct {
	mode, caseName, variant, repeat, output, profile, direction string
	payloadBytes                                                int64
	streamWindowBytes, connectionWindowBytes                    uint64
	rtt, sampleInterval, stepAfter, timeout                     time.Duration
	mbps, reverseMbps, queueBDP, lossPercent, stepMbps          float64
	seed                                                        uint64
}

func bbrLongConfigFromEnv(t *testing.T) bbrLongConfig {
	t.Helper()
	str := func(key, fallback string) string {
		if s, ok := os.LookupEnv(key); ok {
			return s
		}
		return fallback
	}
	number := func(key string, fallback, low, high float64) float64 {
		if s := os.Getenv(key); s != "" {
			v, err := strconv.ParseFloat(s, 64)
			require.NoError(t, err, key)
			require.True(t, v >= low && v <= high, "%s must be in [%g, %g]", key, low, high)
			return v
		}
		return fallback
	}
	c := bbrLongConfig{
		mode: str("BBR_LONG_MODE", "virtual"), caseName: str("BBR_LONG_CASE", "deep"),
		variant: str("BBR_LONG_VARIANT", "current"), repeat: str("BBR_LONG_REPEAT", "1"),
		output: str("BBR_LONG_OUTPUT", ""), profile: str("BBR_LONG_PROFILE", "chromium-h3"),
		direction:      str("BBR_LONG_DIRECTION", "forward"),
		payloadBytes:   int64(number("BBR_LONG_MIB", 200, 1, 1024) * (1 << 20)),
		rtt:            time.Duration(number("BBR_LONG_RTT_MS", 60, 1, 1000) * float64(time.Millisecond)),
		mbps:           number("BBR_LONG_MBPS", 20, 0.1, 1000),
		sampleInterval: time.Duration(number("BBR_LONG_SAMPLE_MS", 250, 10, 1000) * float64(time.Millisecond)),
		seed:           uint64(number("BBR_LONG_SEED", 42, 1, 1<<32)),
		timeout:        time.Duration(number("BBR_LONG_TIMEOUT_SECONDS", 0, 0, 3600) * float64(time.Second)),
	}
	require.Contains(t, []string{"virtual", "wall"}, c.mode)
	require.Contains(t, []string{"deep", "short", "loss", "step"}, c.caseName)
	require.Contains(t, []string{"forward", "reverse"}, c.direction)
	c.reverseMbps = number("BBR_LONG_REVERSE_MBPS", c.mbps, 0.1, 1000)
	c.streamWindowBytes = uint64(number("BBR_LONG_STREAM_WINDOW_MIB", 32, 1, 1024) * (1 << 20))
	c.connectionWindowBytes = 2 * c.streamWindowBytes
	queueBDP, lossPercent, stepSeconds, stepMbps := 4.0, 0.0, 0.0, 0.0
	switch c.caseName {
	case "short":
		queueBDP = 0.25
	case "loss":
		lossPercent = 1
	case "step":
		stepSeconds, stepMbps = 20, 100
	}
	c.queueBDP = number("BBR_LONG_QUEUE_BDP", queueBDP, 0.01, 100)
	c.lossPercent = number("BBR_LONG_LOSS_PERCENT", lossPercent, 0, 10)
	c.stepAfter = time.Duration(number("BBR_LONG_STEP_SECONDS", stepSeconds, 0, 600) * float64(time.Second))
	c.stepMbps = number("BBR_LONG_STEP_MBPS", stepMbps, 0, 1000)
	return c
}

type bbrLongDeparture struct {
	at    time.Time
	bytes int
}

type bbrLongDelivery struct {
	at     time.Time
	packet simnet.Packet
}

type bbrLongLink struct {
	mu             sync.Mutex
	mbps           float64
	queueLimit     int64
	queuedBytes    int64
	departures     []bbrLongDeparture
	finish         time.Time
	random         *rand.Rand
	tailDropped    atomic.Uint64
	randomDropped  atomic.Uint64
	transmitted    atomic.Uint64
	maxQueuedBytes atomic.Int64
	pending        []bbrLongDelivery
	wake           chan struct{}
}

func (l *bbrLongLink) queueSnapshot(now time.Time) (int64, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bytes := l.queuedBytes
	for _, p := range l.departures {
		if p.at.After(now) {
			break
		}
		bytes -= int64(p.bytes)
	}
	return bytes, max(0, l.finish.Sub(now))
}

// The FIFO holds only bytes awaiting serialization (including the packet
// currently in service), not bytes in propagation. Tail drops do not reserve
// service time. Random loss happens after serialization and consumes capacity.
type bbrLongRouter struct {
	simnet.PerfectRouter
	clientPort int
	links      [2]*bbrLongLink
	oneWay     time.Duration
	loss       float64
	stepAfter  time.Duration
	stepMbps   float64
	started    atomic.Int64
	closed     chan struct{}
	workers    sync.WaitGroup
}

func (r *bbrLongRouter) deliver(direction int) {
	l := r.links[direction]
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		l.mu.Lock()
		if len(l.pending) == 0 {
			l.mu.Unlock()
			select {
			case <-l.wake:
				continue
			case <-r.closed:
				return
			}
		}
		p := l.pending[0]
		l.pending[0] = bbrLongDelivery{}
		l.pending = l.pending[1:]
		l.mu.Unlock()
		if delay := time.Until(p.at); delay > 0 {
			timer.Reset(delay)
			select {
			case <-timer.C:
			case <-r.closed:
				return
			}
		}
		select {
		case <-r.closed:
			return
		default:
		}
		// A single worker per direction preserves FIFO even when real timers
		// coalesce. Independent timer callbacks could reorder adjacent packets.
		_ = r.PerfectRouter.SendPacket(p.packet)
	}
}

func (r *bbrLongRouter) Close() {
	close(r.closed)
	r.workers.Wait()
}

func (r *bbrLongRouter) SendPacket(p simnet.Packet) error {
	direction := 0
	if p.From.(*net.UDPAddr).Port != r.clientPort {
		direction = 1
	}
	l := r.links[direction]
	l.mu.Lock()
	now := time.Now()
	i := 0
	for i < len(l.departures) && !l.departures[i].at.After(now) {
		l.queuedBytes -= int64(l.departures[i].bytes)
		i++
	}
	l.departures = l.departures[i:]
	if l.queuedBytes+int64(len(p.Data)) > l.queueLimit {
		l.tailDropped.Add(1)
		l.mu.Unlock()
		return nil
	}
	mbps := l.mbps
	if start := r.started.Load(); direction == 0 && start != 0 && r.stepAfter > 0 && r.stepMbps > 0 && now.Sub(time.Unix(0, start)) >= r.stepAfter {
		mbps = r.stepMbps
	}
	if l.finish.Before(now) {
		l.finish = now
	}
	l.finish = l.finish.Add(time.Duration(float64(len(p.Data)) * 8 * float64(time.Second) / (mbps * 1e6)))
	l.queuedBytes += int64(len(p.Data))
	l.departures = append(l.departures, bbrLongDeparture{at: l.finish, bytes: len(p.Data)})
	l.maxQueuedBytes.Store(max(l.maxQueuedBytes.Load(), l.queuedBytes))
	drop := len(p.Data) > 0 && !wire.IsLongHeaderPacket(p.Data[0]) && l.random.Float64() < r.loss
	if drop {
		l.randomDropped.Add(1)
	}
	l.transmitted.Add(1)
	if !drop {
		l.pending = append(l.pending, bbrLongDelivery{at: l.finish.Add(r.oneWay), packet: p})
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
	l.mu.Unlock()
	return nil
}

func runBBRLongTransfer(t *testing.T, cfg bbrLongConfig) {
	var output *os.File
	var err error
	if cfg.output == "" {
		output, err = os.CreateTemp("", "quic-bbr-long-*.jsonl")
	} else {
		output, err = os.Create(cfg.output)
	}
	require.NoError(t, err)
	defer output.Close()
	encoder := json.NewEncoder(output)
	writeRecord := func(record map[string]any) {
		record["variant"], record["case"], record["repeat"], record["mode"] = cfg.variant, cfg.caseName, cfg.repeat, cfg.mode
		require.NoError(t, encoder.Encode(record))
	}
	t.Logf("BBR long transfer start: variant=%s case=%s mode=%s payload=%d bytes output=%s", cfg.variant, cfg.caseName, cfg.mode, cfg.payloadBytes, output.Name())
	clientAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9201}
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9202}
	router := &bbrLongRouter{clientPort: clientAddr.Port, oneWay: cfg.rtt / 2, loss: cfg.lossPercent / 100, stepAfter: cfg.stepAfter, stepMbps: cfg.stepMbps, closed: make(chan struct{})}
	for i, rate := range []float64{cfg.mbps, cfg.reverseMbps} {
		router.links[i] = &bbrLongLink{
			mbps: rate, queueLimit: max(1500, int64(rate*1e6/8*cfg.rtt.Seconds()*cfg.queueBDP)),
			random: rand.New(rand.NewPCG(cfg.seed, uint64(i+1))),
			wake:   make(chan struct{}, 1),
		}
		router.workers.Go(func() { router.deliver(i) })
	}
	network := &simnet.Simnet{Router: router}
	settings := simnet.NodeBiDiLinkSettings{Downlink: simnet.LinkSettings{MTU: 1500}, Uplink: simnet.LinkSettings{MTU: 1500}}
	clientSocket := network.NewEndpoint(clientAddr, settings)
	serverSocket := network.NewEndpoint(serverAddr, settings)
	require.NoError(t, network.Start())
	defer func() {
		router.Close()
		// Closing sockets unblocks any downlink worker waiting for a QUIC
		// reader that already stopped after cancellation or failure.
		clientSocket.Close()
		serverSocket.Close()
		network.Close()
	}()

	serverTLS, clientTLS := browserTestTLS(t)
	config := &Config{
		MaxIdleTimeout: time.Minute, HandshakeIdleTimeout: 5 * time.Second,
		InitialStreamReceiveWindow: cfg.streamWindowBytes, MaxStreamReceiveWindow: cfg.streamWindowBytes,
		InitialConnectionReceiveWindow: cfg.connectionWindowBytes, MaxConnectionReceiveWindow: cfg.connectionWindowBytes,
	}
	listener, err := Listen(serverSocket, serverTLS, config)
	require.NoError(t, err)
	defer listener.Close()
	budget := time.Duration(float64(cfg.payloadBytes)*8/(min(cfg.mbps, cfg.reverseMbps)*1e6)*float64(6*time.Second)) + 30*time.Second
	if cfg.timeout > 0 {
		budget = cfg.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	clientConfig := config.Clone()
	clientConfig.ClientHelloProfile = cfg.profile
	client, err := Dial(ctx, clientSocket, serverAddr, clientTLS, clientConfig)
	require.NoError(t, err)
	defer client.CloseWithError(0, "")
	server, err := listener.Accept(ctx)
	require.NoError(t, err)
	defer server.CloseWithError(0, "")
	sender, receiver, direction := client, server, 0
	if cfg.direction == "reverse" {
		sender, receiver, direction = server, client, 1
	}
	// The default policy, not a deprecated request bit, must be exercised.
	require.Equal(t, "bbr-v3", sender.ConnectionStats().CongestionControl)
	require.Equal(t, "bbr-v3", receiver.ConnectionStats().CongestionControl)
	require.Equal(t, cfg.profile, client.ConnectionState().ClientHelloProfile)
	started := time.Now()
	router.started.Store(started.UnixNano())
	writeRecord(map[string]any{
		"type": "start", "started_at": started.Format(time.RFC3339Nano), "payload_bytes": cfg.payloadBytes,
		"profile": cfg.profile, "direction": cfg.direction, "forward_mbps": cfg.mbps, "reverse_mbps": cfg.reverseMbps,
		"rate_accounting": "QUIC UDP payload bytes; excludes IP, UDP, and link-layer headers",
		"base_rtt_ms":     cfg.rtt.Seconds() * 1000, "queue_bdp": cfg.queueBDP,
		"queue_bytes_forward": router.links[0].queueLimit, "queue_bytes_reverse": router.links[1].queueLimit,
		"loss_percent": cfg.lossPercent, "seed": cfg.seed, "sample_ms": cfg.sampleInterval.Seconds() * 1000,
		"startup_sample_ms": min(cfg.sampleInterval, 50*time.Millisecond).Seconds() * 1000, "startup_sample_duration_ms": 2000,
		"step_after_ms": cfg.stepAfter.Seconds() * 1000, "step_forward_mbps": cfg.stepMbps,
		"receive_stream_window_bytes":     config.InitialStreamReceiveWindow,
		"receive_connection_window_bytes": config.InitialConnectionReceiveWindow, "deadline_seconds": budget.Seconds(),
		"sustained90_definition": "three consecutive full samples at >=90% of configured bottleneck rate; reported at confirmation time",
	})

	var delivered atomic.Int64
	var streamForStats atomic.Pointer[SendStream]
	type result struct {
		bytes int64
		hash  string
		err   error
	}
	writeDone, readDone := make(chan result, 1), make(chan result, 1)
	go func() {
		stream, err := sender.OpenUniStreamSync(ctx)
		if err != nil {
			writeDone <- result{err: err}
			return
		}
		_ = stream.SetWriteDeadline(started.Add(budget))
		streamForStats.Store(stream)
		digest := sha256.New()
		n, err := io.CopyBuffer(stream, io.TeeReader(io.LimitReader(&bbrLongPatternReader{}, cfg.payloadBytes), digest), make([]byte, 64<<10))
		if err == nil {
			err = stream.Close()
		}
		writeDone <- result{bytes: n, hash: hex.EncodeToString(digest.Sum(nil)), err: err}
	}()
	go func() {
		stream, err := receiver.AcceptUniStream(ctx)
		if err != nil {
			readDone <- result{err: err}
			return
		}
		_ = stream.SetReadDeadline(started.Add(budget))
		digest := sha256.New()
		n, err := io.CopyBuffer(&bbrLongHashWriter{digest: digest, delivered: &delivered}, stream, make([]byte, 64<<10))
		readDone <- result{bytes: n, hash: hex.EncodeToString(digest.Sum(nil)), err: err}
	}()
	timer := time.NewTimer(min(cfg.sampleInterval, 50*time.Millisecond))
	defer timer.Stop()
	lastTime, lastBytes, lastTarget, streak := started, int64(0), 0.0, 0
	var initial90, step90 *float64
	earlyDelivery := make(map[string]any)
	lastProgress := started
	emitSample := func(now time.Time, final bool) {
		elapsed, interval, total := now.Sub(started), now.Sub(lastTime), delivered.Load()
		target := router.links[direction].mbps
		stepped := direction == 0 && cfg.stepAfter > 0 && cfg.stepMbps > 0 && elapsed >= cfg.stepAfter
		if stepped {
			target = cfg.stepMbps
		}
		goodput := 0.0
		if interval > 0 {
			goodput = float64(total-lastBytes) * 8 / interval.Seconds() / 1e6
		}
		if target != lastTarget {
			streak = 0
		}
		if !final {
			if goodput >= .9*target {
				streak++
			} else {
				streak = 0
			}
			if streak >= 3 {
				if stepped && step90 == nil {
					ms := (elapsed - cfg.stepAfter).Seconds() * 1000
					step90 = &ms
				} else if !stepped && initial90 == nil {
					ms := elapsed.Seconds() * 1000
					initial90 = &ms
				}
			}
		}
		s := sender.ConnectionStats()
		r := receiver.ConnectionStats()
		forwardQueueBytes, forwardQueueDelay := router.links[0].queueSnapshot(now)
		reverseQueueBytes, reverseQueueDelay := router.links[1].queueSnapshot(now)
		for _, second := range []int{1, 2, 5} {
			key := strconv.Itoa(second) + "s"
			if _, ok := earlyDelivery[key]; !ok && elapsed >= time.Duration(second)*time.Second {
				earlyDelivery[key] = map[string]any{"sample_elapsed_ms": elapsed.Seconds() * 1000, "delivered_bytes": total}
			}
		}
		record := map[string]any{
			"type": "sample", "elapsed_ms": elapsed.Seconds() * 1000, "interval_ms": interval.Seconds() * 1000,
			"delivered_bytes": total, "delivered_delta_bytes": total - lastBytes, "goodput_mbps": goodput, "target_mbps": target,
			"cwnd_bytes": s.CongestionWindow, "inflight_bytes": s.BytesInFlight, "min_rtt_ms": s.MinRTT.Seconds() * 1000,
			"latest_rtt_ms": s.LatestRTT.Seconds() * 1000, "smoothed_rtt_ms": s.SmoothedRTT.Seconds() * 1000,
			"lost_packets": s.PacketsLost, "lost_bytes": s.BytesLost, "wire_bytes_sent": s.BytesSent, "packets_sent": s.PacketsSent,
			"app_limited_samples": s.ApplicationLimitedRTTSamples, "slow_start": s.SlowStart, "slow_start_exits": s.SlowStartExits,
			"receiver_wire_bytes_sent": r.BytesSent, "receiver_packets_sent": r.PacketsSent,
			"receiver_lost_packets": r.PacketsLost, "receiver_cwnd_bytes": r.CongestionWindow, "receiver_inflight_bytes": r.BytesInFlight,
			"receiver_latest_rtt_ms": r.LatestRTT.Seconds() * 1000, "receiver_smoothed_rtt_ms": r.SmoothedRTT.Seconds() * 1000,
			"receiver_slow_start": r.SlowStart, "receiver_slow_start_exits": r.SlowStartExits, "receiver_app_limited_samples": r.ApplicationLimitedRTTSamples,
			"taildrop_forward": router.links[0].tailDropped.Load(), "taildrop_reverse": router.links[1].tailDropped.Load(),
			"randomdrop_forward": router.links[0].randomDropped.Load(), "randomdrop_reverse": router.links[1].randomDropped.Load(),
			"queue_bytes_forward": forwardQueueBytes, "queue_bytes_reverse": reverseQueueBytes,
			"queue_delay_ms_forward": forwardQueueDelay.Seconds() * 1000, "queue_delay_ms_reverse": reverseQueueDelay.Seconds() * 1000,
			"final_partial_sample": final,
		}
		if stream := streamForStats.Load(); stream != nil {
			// SendStream serializes its flow controller through this mutex;
			// SendWindowSize also takes the connection's send-side mutex.
			stream.mutex.Lock()
			record["stream_sent_bytes"] = stream.flowController.bytesSent
			record["stream_send_limit_bytes"] = stream.flowController.sendWindow
			record["stream_send_credit_bytes"] = stream.flowController.SendWindowSize()
			stream.mutex.Unlock()
		}
		writeRecord(record)
		if now.Sub(lastProgress) >= 10*time.Second {
			t.Logf("progress %s: %.1fs, %.1f MiB delivered, %.2f Mbps, cwnd=%d inflight=%d lost=%d", cfg.variant, elapsed.Seconds(), float64(total)/(1<<20), goodput, s.CongestionWindow, s.BytesInFlight, s.PacketsLost)
			lastProgress = now
		}
		lastTime, lastBytes, lastTarget = now, total, target
	}
	var received result
	for running := true; running; {
		select {
		case <-timer.C:
			emitSample(time.Now(), false)
			interval := cfg.sampleInterval
			if time.Since(started) < 2*time.Second {
				interval = min(interval, 50*time.Millisecond)
			}
			timer.Reset(interval)
		case received = <-readDone:
			running = false
		case <-ctx.Done():
			received = result{bytes: delivered.Load(), err: ctx.Err()}
			running = false
		}
	}
	finished := time.Now()
	emitSample(finished, true)
	var sent result
	select {
	case sent = <-writeDone:
	case <-ctx.Done():
		sent.err = ctx.Err()
	}
	success := sent.err == nil && received.err == nil && sent.bytes == cfg.payloadBytes && received.bytes == cfg.payloadBytes && sent.hash == received.hash
	summary := map[string]any{
		"type": "summary", "success": success, "payload_bytes": cfg.payloadBytes, "sent_bytes": sent.bytes, "received_bytes": received.bytes,
		"elapsed_ms": finished.Sub(started).Seconds() * 1000, "goodput_mbps": float64(received.bytes) * 8 / finished.Sub(started).Seconds() / 1e6,
		"sender_sha256": sent.hash, "receiver_sha256": received.hash, "initial_sustained90_at_ms": initial90, "step_sustained90_after_ms": step90,
		"early_delivery":   earlyDelivery,
		"taildrop_forward": router.links[0].tailDropped.Load(), "taildrop_reverse": router.links[1].tailDropped.Load(),
		"randomdrop_forward": router.links[0].randomDropped.Load(), "randomdrop_reverse": router.links[1].randomDropped.Load(),
		"max_queue_bytes_forward": router.links[0].maxQueuedBytes.Load(), "max_queue_bytes_reverse": router.links[1].maxQueuedBytes.Load(),
	}
	if sent.err != nil {
		summary["send_error"] = sent.err.Error()
	}
	if received.err != nil {
		summary["receive_error"] = received.err.Error()
	}
	writeRecord(summary)
	require.NoError(t, output.Sync())
	t.Logf("BBR long transfer complete: success=%v elapsed=%s goodput=%.2f Mbps output=%s", success, finished.Sub(started), summary["goodput_mbps"], output.Name())
	require.True(t, success, "stream length / SHA256 / completion failed; inspect %s", output.Name())
}

type bbrLongPatternReader struct{ offset uint64 }

func (r *bbrLongPatternReader) Read(p []byte) (int, error) {
	for i := range p {
		pos := r.offset + uint64(i)
		p[i] = byte(pos ^ pos>>8 ^ pos>>16 ^ pos>>24)
	}
	r.offset += uint64(len(p))
	return len(p), nil
}

type bbrLongHashWriter struct {
	digest    hash.Hash
	delivered *atomic.Int64
}

func (w *bbrLongHashWriter) Write(p []byte) (int, error) {
	n, err := w.digest.Write(p)
	w.delivered.Add(int64(n))
	return n, err
}

var _ simnet.Router = (*bbrLongRouter)(nil)
var _ io.Reader = (*bbrLongPatternReader)(nil)
var _ io.Writer = (*bbrLongHashWriter)(nil)
