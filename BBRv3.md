# BBRv3 for QUIC

This fork provides an independent userspace QUIC implementation of the BBRv3 algorithm described in [draft-ietf-ccwg-bbr-06, July 2026](https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html). The controller is implemented in `internal/congestion/bbrv3_sender.go`; it does **not** embed the older `bbrSender`, reuse its eight-phase ProbeBW cycle, or relabel BBRv1 as BBRv3.

This is a QUIC adaptation, not Google's Linux TCP module and not a claim of certification, formal equivalence, or production-scale fairness evaluation. The Internet-Draft is a work in progress; the implementation targets revision 06 rather than silently tracking future draft changes.

## Default: lightly tuned BBRv3

On the `perf/bbrv3-default-20260923` development branch, a nil or zero Config uses lightly tuned BBRv3 automatically. No algorithm selection is needed. Controller choice remains local to the sender, not a wire negotiation. `Config.CongestionControlName()` and `ConnectionStats().CongestionControl` report `bbr-v3`, including after path migration.

The reprobe interval is **1–2 seconds** instead of 2–3 seconds, with **10%** rather than 15% long-term headroom. The recovery candidate additionally uses an **8% long-term probing loss threshold only on established paths when both latest and smoothed RTT remain within `max(1 ms, minRTT/8)` of the current minimum**. Startup, missing RTT evidence, and inflated RTT keep the original **2%** threshold. This is a queue-delay heuristic, not proof that losses are non-congestive; it is specific fork tuning, not the IETF draft's policy. Short-term bounds still respond to every loss round. Startup/UP/DOWN gains, the 0.7 reduction factor, cwnd limits and ProbeRTT scheduling remain unchanged.

The application-limited filter now distinguishes a transient empty producer queue from a full network pipe in every ProbeBW phase. At or above 90% of the quantized model BDP, a transient yield does not suppress delivery-rate samples; Startup retains its existing 1.5-BDP check. Truly under-filled pipes still use application-limited filtering. This allows stale high-rate estimates to age after a capacity drop. Stream retransmissions additionally repair the lowest pending stream offset first, while first-loss batches keep the append fast path. No receive-gap or queue bound is enlarged.

These changes favor useful delivery and recovery, not an unpaced or loss-ignoring sender. They require application and real-path validation; reduced fairness or more aggressive probing is not itself evidence of improved performance.

For source compatibility, existing EnableBBR/EnableCubic fields and helper methods remain deprecated aliases. Helpers preserve their historical request bits for callers that inspect them, but populated configs and every production handler select v3. Contradictory old flags no longer select different algorithms. Historical controller code/tests are retained as references, not reachable selectable policies. Existing peers need no wire-protocol change, but a caller requiring exact old congestion behavior must stay on its previous version.

The immutable `v0.63.0-quic.2` release has not been rewritten. See [the current implementation and test record](BBRV3_TUNING_20260923.md) before adopting this candidate.

## Specification mapping

| Algorithm area | Implementation and checks |
| --- | --- |
| STARTUP and DRAIN | Pacing gains 2.77 and 0.5, packet-timed full-bandwidth plateau detection, app-limited filtering, round-bounded drain, startup high-loss exit based on a full loss round and multiple loss ranges. |
| ProbeBW | Separate DOWN, CRUISE, REFILL, and UP states, with ACK-phase tracking so delayed feedback is attributed to the sending phase. Randomized time/round probe spacing and precautionary reprobes are modeled explicitly. |
| Maximum delivery rate | Two completed probe-cycle bandwidth windows; low app-limited samples do not erase an already established path model. Sent-packet delivery state is used rather than ACK arrival spacing alone. |
| Congestion bounds | Short-term bandwidth and in-flight bounds, long-term in-flight bounds, 2% base / conditionally 8% probing-loss threshold as described above, 0.7 reduction factor, and tuned 10% long-term headroom outside upward probing. |
| Upward probing | ACK-clocked, cwnd-limited long-term-bound growth with an exponential round-by-round slope; exit on excess loss or a bandwidth plateau. |
| Minimum RTT and ProbeRTT | A ten-second minimum-RTT window and five-second probe scheduling interval; ProbeRTT uses half the estimated BDP, at least four datagrams, for at least 200 ms and one packet-timed round. Saved cwnd is restored afterward. |
| ACK aggregation | A bounded excess-ACK model adds headroom to the BDP target without treating compressed ACK bursts as new measured bandwidth. |
| Idle restart | Preserves the path model, resets aggregation accounting, and avoids a redundant immediate ProbeRTT when the pipe has already drained while app-limited. |
| Pacing and cwnd | Model-based rate with the 1% pacing margin, per-state cwnd gains and volume/quantization bounds, minimum/maximum cwnd, and saturating arithmetic. |
| Recovery and packet lifetime | Sent metadata is retired exactly once on ACK, declared loss, or packet-number-space discard. Recovery epochs use the connection-wide packet sequence, and changing MTU updates datagram-scaled bounds. |

Deterministic unit tests cover each area, including application-limited startup, phase gains, two-cycle filtering, loss-threshold interpolation from transmission-time state, startup high-loss rounds, bound recovery, RTT expiry, ACK compression, duplicate/discard lifecycle, MTU changes, and overflow/tiny-BDP cases.

## QUIC-specific decisions

The controller receives QUIC's acknowledged/lost byte events and delivery samples. It shares the existing delivery-rate sampler, monotonic clock, and pacing infrastructure, not BBRv1's control law. The adapter uses a connection-wide congestion packet sequence so Initial, Handshake, and application packet-number spaces cannot collide in recovery/sampling metadata.

QUIC packet-number-space discard is not congestion loss. ACK-only packets are not treated as new in-flight data. Lost/acknowledged accounting uses the actual retained packet metadata; duplicate callbacks cannot create delivery samples or repeatedly lower bounds. Network path migration constructs a new v3 controller and resets the old path's model rather than carrying an unrelated bottleneck estimate to a new route.

BBRv3 mode does not enable ECN negotiation: this implementation does not yet define a CE-based control response. It therefore does not advertise an ECN response and then ignore congestion marks. Loss-based BBRv3 operation remains available.

A conservative recovery-undo helper is tested for transports that can prove an entire loss episode spurious. The current QUIC adapter does **not** infer that proof from a single late ACK, so it does not invoke a potentially incorrect wholesale model undo. This is a deliberate transport-specific difference from TCP integrations with richer undo signals.

Pacing burst quantization follows this QUIC stack's datagram scheduler, not Linux TCP GSO/TSO internals. It is not appropriate to compare an individual implementation constant to a TCP module and infer identical packet trains. Pacing or RTT precision is also limited by the host scheduler.

## End-to-end regression coverage

`TestBBRv3RealQUICDuplexAndIdle` opens real loopback QUIC sockets and verifies **10 MiB in each direction** per scenario, including four simultaneous bidirectional streams and an application-idle interval longer than the ProbeRTT interval. It repeats with a deterministic dropped packet every 97 received datagrams on **both** endpoints. SHA-256 checks guard data integrity, both live connections must report `bbr-v3`, and the loss-injection case asserts that packets were actually dropped.

The fork's full existing QUIC, HTTP/3, loss recovery, and integration suites remain applicable. A successful local or WAN run establishes behavior only for the tested conditions; it is not a throughput guarantee, congestion fairness result, or proof of absence of defects. Tailcat's own release validation separately covers the H3 application transport, credentials, and release binaries.

## Source and license attribution

The primary algorithm reference is the IETF BBRv3 draft linked above. [Google's BBRv3 TCP implementation](https://github.com/google/bbr/blob/v3/net/ipv4/tcp_bbr.c) is a secondary reference for interpretation; no Linux kernel dependency is introduced.

The new controller is distributed under BSD-3-Clause as stated in its file header. Existing quic-go code retains its [MIT license](LICENSE). To preserve attribution for algorithm/code-component adaptations, the IETF code-component notice is included below. Copyright in the referenced specification belongs to the IETF Trust and the persons identified as its authors; this does not imply their endorsement of this fork.

### BSD code-component notice

Copyright (c) 2026 IETF Trust and the persons identified as authors of the referenced code components. All rights reserved.

Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the following disclaimer in the documentation and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors may be used to endorse or promote products derived from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
