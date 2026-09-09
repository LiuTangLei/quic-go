# Datagram BBR controller

This fork keeps the legacy BBRv1-derived controller behind
`Config.EnableBBRCongestionControl()`, and adds a separate opt-in
`Config.EnableBBRv3CongestionControl()` for the explicit BBRv3 state machine.
Both controllers are local congestion policies. They use per-packet delivery
sampling, pacing, congestion-window and loss-recovery limits. The normal
default controller and the on-wire protocol are unchanged.

## BBRv3-inspired tuning

The explicit BBRv3 controller follows the draft's startup, drain, ProbeBW
phases, RTT probing and app-limited / idle restart behaviors as implemented in
this worktree. The legacy BBRv1-derived sender remains available for
compatibility and is not relabeled or merged into the v3 path.

The following changes borrow the bounded startup, idle-restart and RTT-probing
ideas in the [BBRv3 draft, revision 06](https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html):

- Startup pacing gain is 2.77, with a separate 2x BDP window gain. The initial
  window remains 32 datagrams. Drain uses a 0.5 pacing gain.
- Normal pacing includes a 1% margin. After application idle, ProbeBW restarts
  at the estimated delivery rate with that margin and resets its ACK epoch,
  retaining the previous bandwidth model for small reverse-direction traffic.
- ProbeRTT targets half the estimated BDP, with a floor of four path-sized
  datagrams. This avoids always collapsing a high-BDP tunnel to four packets.
  It keeps the previous unloaded RTT as its drain baseline, then measures a new
  candidate after reaching the target. Raising the RTT estimate requires both
  200 ms and a fresh packet-timed round after draining. The existing 10-second
  expiry is retained.

This is **not a complete BBRv3 implementation**. ProbeBW retains the eight-phase
BBRv1 gain cycle. The v3 long/short-term bandwidth and inflight bounds, loss-range
startup exit, adaptive refill/up/cruise cycles, and spurious-loss model undo have
not been ported. A single ECN indication retains the existing recovery response;
it is not treated as a measured v3 ECN fraction. Diagnostics retain the existing
`bbr-v1` identifier to identify this controller family.

## Correctness fixes

Congestion during a high-gain probe is remembered until that probe has lasted a
minimum RTT, allowing it to drain even when loss keeps flight below its target.
Per-packet loss callbacks from the same ACK cannot advance through several
phases. ECN reports with zero lost bytes no longer inflate packet-loss counters
or discard a valid delivery sample.

ACK aggregation and BDP calculations avoid multiplying byte rates by integer
nanoseconds. At 1 Gbps, the former calculation could overflow after approximately
74 seconds and turn a 120-second gap into about 3.4 GB of false ACK compensation.
Compensation is bounded by the congestion window, and BDP gains are applied
before the final maximum-window cap. ProbeRTT pacing can decrease even if
startup has not yet declared full bandwidth.

The 200 MiB experiments additionally exposed two interactions with the QUIC
send loop. During startup, a chunked writer's momentarily empty queue does not
mark the connection application-limited when a valid bandwidth model already
has at least 1.5 BDP in flight. A packet-timed round can use its first valid
non-application-limited ACK for the plateau check, even if its boundary ACK was
application-limited. Each round still counts at most once; sparse traffic and
ProbeRTT retain their protections.

ACK-only packets do not debit BBR's pacing budget. QUIC permits pure ACKs while
data is pacing-limited; charging those ACKs could otherwise keep postponing
MAX_DATA or MAX_STREAM_DATA on a receiver with a low-rate control-traffic model.
Data and ack-eliciting control packets continue to consume the normal budget.

## Validation and use with Tailscale

`internal/congestion/bbr_v3_tuning_test.go` covers the specific failures and
phase/window behavior. `bbr_startup_app_limited_test.go` and the ACK-only pacing
regression cover long-transfer failures. Existing sampler, reverse-traffic, ACK-handler, PMTU,
handshake, and HTTP/3 suites remain applicable. Run:

```sh
GOTOOLCHAIN=go1.26.6 go test ./...
GOTOOLCHAIN=go1.26.6 go test -race ./internal/congestion ./internal/ackhandler
```

The Tailscale H3 consumer pins a published fork version. Paired development
tests must explicitly use this checkout through a temporary modfile or Go
workspace; changing this checkout alone does not update that version or a
running daemon. Unit and simulated-network results do not establish WAN
throughput, production deployment, or fairness against competing controllers.
