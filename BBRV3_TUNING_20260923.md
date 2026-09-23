# Lightly tuned BBRv3: implementation and validation

Date: 2026-09-23. Development branch: `perf/bbrv3-default-20260923`.
Base: `3859dc05517a8dd5fb46a4e549e880a053b78455`, which already contains the
bounded DATAGRAM group allocation and event-driven, actual-FIN-ACK fixes.
This work does not rewrite a published tag or install production binaries.

## One production congestion controller

A nil/zero Config, legacy selection helpers, contradictory legacy flag combinations,
and connection migration now all instantiate the same lightly tuned BBRv3 sender.
`CongestionControlName()` and live connection statistics keep the stable name
`bbr-v3`. Applications need no new configuration field, selector or wire negotiation.

The old exported EnableBBR/EnableCubic fields and helper names remain deprecated
source-compatibility shims. Some existing Tailscale integration code inspects the
requested bit before Dial, so the old helper preserves that request bit. At Config
normalization and actual sender construction, it always becomes BBRv3. Those old
names no longer select BBRv1, CUBIC or Reno. Reference implementations and their
unit tests remain in the internal package; production construction/migration no
longer references their constructors. Source compatibility does not promise the
old congestion behavior; consumers requiring it must retain their old dependency.

Congestion control is distinct from receiver stream/connection flow control.
HTTP/3 flow-control windows, authenticated peer admission, TLS/QUIC encryption,
anti-amplification, ACK/loss recovery and bounded application queues are preserved.
The previously implemented BBRv3 integration does not implement an ECN CE response,
so it continues not to advertise ECN capability rather than silently ignoring CE.

## The limited tuning

| Parameter | Previous fork v3 policy | Candidate |
| --- | --- | --- |
| Time-based bandwidth reprobe wait | 2 to <3 seconds | 1 to <2 seconds |
| Long-term inflight headroom | 15% (at least one datagram) | 10% (at least one datagram) |
| UP pacing gain | 1.25 | unchanged |
| Startup / drain gain | 2.77 / 0.5 | unchanged |
| Loss threshold / reduction factor | 2% / 0.7 | unchanged |
| ProbeRTT | existing 5-second scheduling, 200 ms minimum duration | unchanged |

The intent is quicker rediscovery of spare capacity and slightly less unused
inflight margin, not reproducing the draft's exact coexistence tradeoff. Random
probe jitter remains. Probe loss still exits UP, and minimum/maximum windows,
saturating arithmetic, pacing and recovery remain enforced. More aggressive
probing may increase competing-flow pressure or tail latency; no fairness or
all-path speed improvement is claimed.

Algorithm baseline: [IETF BBR draft revision 06](https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html).
That draft is experimental and is not a certification of this adaptation.

## Lower CPU/allocation cost without changing delivery arithmetic

`ConnectionStates` now stores compact sent-state values inline in the existing
packet-number map instead of allocating a separate record for every packet.
`BandwidthSample` is returned by value rather than as a newly allocated object.
ACK removes and retrieves its record together before calculating the same sample;
BBRv3 retains a value snapshot when it needs transmission-time metadata.

This is not FIFO retirement. ACKs can arrive out of order, and lost/discarded or
duplicate packet notifications cannot release unrelated records or inflate byte
accounting. Snapshot tests cover reverse ACK order, loss, discard, duplicate
insertion/ACK/loss, ACK-only packets and retained copies. A size guard covers
future growth that would defeat inline map storage in the tested Go runtime.

The map still allocates on initialization/growth and retains bucket capacity.
Zero steady-state per-packet objects is not zero connection memory, a bounded
whole-process RSS promise, or removal of all allocation throughout QUIC.

### Same-fixture microbenchmarks

Apple M4, Go 1.27.1, GOMAXPROCS=1. Three 200 ms samples per case, comparing base
3859dc05 with the candidate. The same benchmark source is overlaid on a clean
baseline worktree; runtime baseline files are not edited. Each operation sends
and acknowledges a full synthetic flight of 32 or 512 packets, with 50 ms virtual
RTT. It does not exercise real UDP sockets, packet encryption or application code.

| Case | Baseline median ns/flight | Candidate median ns/flight | Allocations baseline / candidate |
| --- | ---: | ---: | ---: |
| Sampler, 32 packets | 2698 | 1862 | 64 / 0 |
| BBRv3 bookkeeping, 32 packets | 5604 | 4485 | 64 / 0 |
| Sampler, 512 packets | 52613 | 29691 | 1024 / 0 |
| BBRv3 bookkeeping, 512 packets | 109455 | 72216 | 1024 / 0 |

Raw ns/flight samples:

- Sampler/32: base 2411, 2844, 2698; candidate 1812, 1862, 1867.
- BBRv3/32: base 8104, 5271, 5604; candidate 4465, 4501, 4485.
- Sampler/512: base 41748, 52613, 68928; candidate 29691, 29691, 30220.
- BBRv3/512: base 109455, 115991, 107683; candidate 92431, 72216, 70750.

32-packet allocation bytes drop from 4096 B/op to 0 B/op. At 512 packets, baseline
is about 65541–65583 B/op and candidate about 20–70 B/op, representing amortized
map initialization/growth. The samples contain scheduler/load variation. The
BBRv3-case median reductions are about 20% and 34%, not measured whole-process
CPU reductions or WAN throughput gains. Baseline and candidate also differ in the
probe tuning, so sampler-only figures better isolate the storage change.

## Compatibility and correctness validation

- Final `go test -short ./...` passes under Go 1.27.1.
- QUIC / HTTP3 / ACK handling / congestion / TLS short race suites pass.
- `go vet ./...`, formatting and diff checks pass.
- Whole-tree Windows amd64 and Linux ARMv7 cross-builds pass.
- Zero/nil configs, all eight legacy flag combinations, helper aliases and actual
  handler construction/migration report/select only BBRv3.
- The real socket duplex test deliberately no longer opts in to BBRv3. Default
  client/server controllers are checked, with 10 MiB in each direction per
  scenario, concurrent streams and idle recovery, with and without deterministic
  loss every 97 datagrams on each endpoint. Both scenarios pass with integrity.
- The serialized 4 Mbps / 60 ms virtual link test with deterministic 1% packet
  drops completes both directions and resumes after 12 seconds idle, for standard
  and Chromium-inspired handshakes. Recorded virtual goodput is approximately
  3.39–3.59 Mbps; this is a simulated recovery regression, not Internet speed.
- Tailscale integration at b4f1aa3cd passes wgtransport and subpackages, quicip,
  and transportprofile tests with the candidate QUIC module.
- Tailcat f4af70cbe, using its unchanged public integration pin, passes its complete
  application/CLI/web suite and serialized race suite with the candidate module.
  Consumer go.mod files remain unchanged; overrides live in external test modfiles.

### A failure that is not hidden

The first full short run in this continuation hit a single stochastic
`TestHandshakeWithPacketLoss` timeout at handshake_drop_test.go:113: 1/3 packet
loss in both directions, no Retry, server speaks first. The failing path did not
finish its server-first transfer within the virtual-time budget. That test was
not deleted, skipped, loosened or changed to select another controller.

Twenty complete repetitions on this candidate and twenty on the clean 3859dc05
baseline then passed. A final full short run also passed. The original random
seed was not logged, so the exact failing loss schedule was not reproduced and
no root-cause fix or proof of flakiness is claimed. Keep this as an open stress
validation item before calling the change production-ready.

## Continuation: fewer ACK lookups and explicit old/new interop

The continuation on the same branch removes another redundant sent-state map
lookup from the BBRv3 ACK path. The sender now takes its original send record
once and invokes the same sampler arithmetic on that owned value. Missing,
duplicate and discarded packet callbacks still do not inflate delivery. New
sender-level tests cover reversed ACK order interleaved with duplicate loss,
late ACK and discard, plus an invalid timestamp that must still account for
exactly one delivered packet. Both maps end empty after packet retirement.

Same Apple M4, Go 1.27.1, GOMAXPROCS=1, three 300 ms samples, comparing the
immediately preceding e9696a4e implementation against this small ACK-only change:

| BBRv3 bookkeeping | Before median ns/flight | After median ns/flight | Reduction |
| --- | ---: | ---: | ---: |
| 32 packets | 4261 | 3773 | 11.5% |
| 512 packets | 68439 | 60491 | 11.6% |

Before raw samples: 4250 / 4274 / 4261 and 68277 / 68439 / 68792.
After raw samples: 3773 / 3773 / 3767 and 60600 / 60491 / 60240.
Steady-state allocations remain zero per reported operation. These percentages
must not be added to earlier independent benchmark percentages or described as
whole-process CPU or WAN throughput gains.

A separate test-only loopback helper compiled against the immutable published
v0.63.0-quic.2 and the candidate. It exercises seven endpoint combinations twice:
new/new defaults, and both orientations with an old default, old BBRv1 caller
and old BBRv3 caller. All 14 final runs passed. Each used certificate-verified
TLS/QUIC on 127.0.0.1, four concurrent reliable streams with 2 MiB echo content
in total, and 32 checked 512-byte DATAGRAM echoes. Every new endpoint reported
bbr-v3. This is library wire interoperability, not an additional Tailscale node
authentication, NAT/relay or Internet benchmark.

The first harness iteration failed 5/14 runs because its server sent
CONNECTION_CLOSE immediately after a final transport ACK. That ACK did not
prove the peer application had consumed its buffered control message. The
harness now requires an explicit application completion receipt, drains the
client's final write, and lets the client initiate connection close. No library
runtime code or content assertion was changed to make the harness pass. The
initial failed JSON and final result remain separate in the private audit
loopback-interop directory; the initial failure is not relabeled successful.

Added TestBBRv3DefaultSeededHandshakeLoss supplements the unchanged upstream
random loss matrix. Four seeds, both Retry settings and both first-speaker
roles make 16 cases; three complete repetitions passed (48 case executions).
Each direction has its own seeded one-third drop sequence, capped at ten
consecutive drops, and failures retain seed and direction information. It uses
virtual network time. This does not reproduce the earlier unknown random seed
or establish a fix for that isolated historical timeout.

The complete short suite, QUIC/H3/ACK/congestion/TLS race suites, go vet and both
consumer test suites were rerun for the final runtime change. Tailscale's
related authentication/close/batch race tests and Tailcat's complete serialized
race suite also passed. Neither consumer go.mod was modified.

## Build identities and WAN evidence limits

The resumed Linux Tailcat test build has SHA-256
`3b3d48b5ff92df5a9633961e56b7aacc5319f601c92241fa9f1bfa5289a1e9bc`;
its macOS arm64 counterpart is
`daf1f6bf2e5f9be8d87b2c7e4fd12303e70ebc14fc337234347240fc38ac9208`.
These are explicitly test-only artifacts, not a republished v0.7.0-quic.2.

Earlier saved WAN candidate records under `bbrv3-default-20260923` refer to Linux
hash `cc6f68d515e8096b89e4d8afc11eb35815f37926e55b474ce7f82b2ddab4d63c`.
Their build is not byte-identical to the resumed candidate; additionally one
record lacks a final cleanup result. They are retained as historical evidence
and are NOT used as throughput/CPU acceptance for the final tree.

A fresh bounded Tailcat AU/US test launch using the resumed binary was blocked
by the execution tool. It was not retried through another agent, host or runner.
No fresh WAN throughput number, equal-load process CPU reduction, mixed-version
WAN matrix or multi-hour stress result is claimed for this candidate. The later
loopback-only old/new interoperability check is recorded above.

Artifacts and test-only module files are on the Mac under
`tailscale-all/audits/bbrv3-default-20260923`. The validated development source is
also fast-forwarded to the shared library default branch; the published
v0.63.0-quic.2 tag, production binaries, application pins, node state and disabled
library Actions settings are not changed. Default-branch development code is
not a newly published or WAN-performance-qualified application release.
