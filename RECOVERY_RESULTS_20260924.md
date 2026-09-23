# BBRv3 recovery fixes and measured limits — 2026-09-24

Runtime change: `7ca60b742f2f23765ef3cce85729e5103289147f`.
Comparison: `264ca424` (test/docs updates on runtime `4600d8a2`).
Branch: `fix/bbrv3-recovery-20260924`. This is not a release or deployment.

## Changes

1. A transient empty application queue no longer marks a full network pipe
   application-limited throughout ProbeBW. This permits delivery samples to
   age stale high-capacity estimates after a bandwidth drop. Startup keeps
   the existing 1.5-BDP condition; other phases use the quantized 0.9-BDP
   condition. Actually under-filled pipes remain application-limited.
2. Established paths use an 8% probing-loss threshold only when both latest
   and smoothed RTT are close to the minimum. Startup, missing RTT evidence,
   and queue-delay indications keep 2%. The exact heuristic and caveats are
   in BBRv3.md. Short-term bounds still react to loss rounds; ACK/loss
   detection, pacing, reduction factors and queue/gap limits are not removed.
3. Lost STREAM data is queued by stream offset, so an old retransmission lost
   again repairs the earliest hole before newer pending bytes. First-loss
   batches retain the append fast path. Reordered insertion moves pointers,
   not payloads; it is O(n) in the exceptional insertion case. Removed queue
   entries are cleared to avoid retaining stale frame references.

No wire format, encryption, authentication or public configuration selector
changed. The controller remains the sole default lightly tuned BBRv3.

## Reproduced failures and repaired cases

The same local virtual-link harness transfers 200 MiB, preserves the original
automatic timeout, and requires length and SHA-256 equality. The link-loss
seed is fixed; controller probe jitter and goroutine scheduling are not fully
seeded. Rates account for QUIC payloads, not every outer link header.

| Condition | Before | After |
| --- | --- | --- |
| 100 to 20 Mbps at 5 s, RTT 60 ms, seed 42 | Gap-limit abort after 114,867,805 received bytes | All 209,715,200 bytes verified in 67.13 virtual seconds |
| 20 Mbps, RTT 60 ms, 5% short-header packet loss in each direction, seed 42 | Timeout after 533.22 virtual seconds; 139,915,115 bytes received | All bytes verified in 115.32 virtual seconds; 14.55 Mbps |

The failed baseline's partial rates are not treated as completed-transfer
benchmark scores. The capacity-drop completion time includes the initial
100 Mbps phase and does not imply steady delivery above the later 20 Mbps cap.

Final matrix: **16/16** cases, each **200 MiB**, totaling **3,355,443,200
verified bytes**. It covers seeds 1/7/42/97, forward and reverse transfers,
deep and shallow queues, 1% and 5% loss, capacity increase/decrease, and a
200 ms RTT check. At 60 ms, completed 5% loss runs delivered approximately
13.92–14.96 Mbps. At 200 ms, the tested forward 5% loss case completed at
7.81 Mbps: high RTT still materially reduces throughput.

The early experiment that unconditionally changed 2% to 8% improved the
5% loss case but reintroduced the gap-limit abort after a capacity drop.
It was rejected, and both its passing and failing records are retained.

The permanent non-short `TestBBRv3RecoveryLongTransfer` now covers the two
200 MiB regressions. The random-loss case also requires more than 8 Mbps,
preventing a much slower but eventually complete transfer from hiding the
original collapse. Unit regressions cover application-limited classification,
RTT-dependent loss bounds, short-term loss response and old-hole repair.

## CPU and healthy-path comparison: no proven general speedup

Same Apple M4; same compiled Tailscale test at `b4f1aa3cd300`; before/after
QUIC only; three alternating rounds per variant, each 200 MiB in each
direction. This uses actual local sockets and userspace TCP -> authenticated
H3 CONNECT-IP -> QUIC DATAGRAM, with the Chromium-inspired profile verified.
It is neither an Internet benchmark nor the production OS TUN data path.

| Median | Before | After |
| --- | ---: | ---: |
| Ordinary -> declared server | 842.72 Mbps | 811.22 Mbps |
| Declared server -> ordinary | 819.11 Mbps | 822.62 Mbps |
| Test-process CPU time per complete bidirectional run | 29.59 CPU-s | 29.66 CPU-s |

All six runs completed and verified both directions. CPU time includes both
temporary nodes, control/DERP setup and file preparation, but excludes
compilation. It is cumulative CPU-seconds, not a percentage or a single
production endpoint's cost. Three rounds do not establish a statistically
significant small difference. No healthy-path CPU reduction or universal
throughput gain is claimed; the forward median is lower, not omitted.

The existing GOMAXPROCS=1 BBRv3 bookkeeping benchmark, five 250 ms samples
per variant on Go 1.27.1, measured median batch costs of 3711 -> 3711 ns for
32 packets and 59722 -> 59657 ns for 512 packets: effectively unchanged.

## Verification

- Full ordinary and serialized race QUIC suites: 2704 passed test/subtest
  events, four skipped, no failures. The opt-in long helper was executed
  separately; Linux-only GSO/sendmsg cases were not executed on macOS.
- The first full runs failed one existing RESET_STREAM_AT test because it
  asserted the former FIFO order. Its expectations now check earliest offset
  first while preserving reliable-boundary truncation, content, cancellation
  and completion checks. The initial failures remain in the audit.
- Go 1.26 focused recovery regressions passed; go vet passed.
- Library builds passed for Linux amd64/arm64/ARMv7, macOS amd64/arm64 and
  Windows amd64/arm64. These are cross-builds, not native device execution.
- Tailscale relevant integration: 657 passed / three skipped; targeted
  transport race checks: 210 passed. The opt-in bulk helper was separately
  exercised by the six-round real-socket local comparison.
- Tailcat browser-fix consumer `d360f529b`: full and race suites each 266
  passed, five browser cases skipped by default. Those five real-Chrome
  cases were then enabled, with candidate QUIC also inherited by the WASM
  build: two rounds, 10/10 passed. Official dependency pins were unchanged.

Records and scripts are in the owner's local `audits/quic-recovery-20260924/`.
Browser connection codes are capabilities; raw browser logs are private, not
part of this public report. SUMMARY.json includes log hashes and all final
stress summaries, plus the rejected experiments and application samples.

## Limits

No new WAN acceptance, multi-hour soak, native Windows/Android, Safari/Firefox
or native Linux GSO run was performed. A historical intermittent Linux
corrupted-handshake timeout is not claimed root-caused by these changes.
The RTT gate is a heuristic, not proof of non-congestive loss or fairness.
No new release/tag, application dependency pin, production daemon or GitHub
Actions configuration is changed by this branch.
