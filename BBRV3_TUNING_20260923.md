# Lightly tuned BBRv3 and lower-overhead sampling — 2026-09-23

## Scope and release status

Branch: `perf/bbrv3-default-20260923`, based on `3859dc05` (the preceding DATAGRAM group-allocation and event-driven FIN-ACK candidate). Published `v0.63.0-quic.2` and the default branch are not rewritten. No application dependency pin or production service is changed by this candidate.

The public introduction is deliberately simple: **lightly tuned BBRv3**. Tailscale and Tailcat remain independent consumers of this shared library.

## One production policy

Nil/zero Config, explicit v3 configuration, legacy flags and legacy setter calls all create BBRv3. Path migration also creates BBRv3 directly. The old code used to construct a Reno/CUBIC object before replacing it with BBR; that redundant construction is gone. There is no selectable Reno/CUBIC/BBRv1 branch in production handler creation or migration.

Old fields and setters are retained as deprecated source-compatibility aliases. Some existing Tailscale integration tests inspect EnableBBR immediately after calling the old helper, so that historical request bit is preserved until configuration population. The active normalized flags and actual controller still select v3, and diagnostics always say `bbr-v3`. Keeping a field name does not keep an alternative algorithm. Historical implementation/reference tests remain in the source tree; they are not advertised production choices.

This changes congestion behavior intentionally, not QUIC negotiation or packet framing. Code that requires exact old controller behavior must retain its previous dependency version. No mixed old/new WAN test is claimed here.

## Deliberately small tuning

| Parameter | Previous fork v3 | Candidate |
| --- | --- | --- |
| Randomized bandwidth reprobe wait | 2–3 seconds | 1–2 seconds |
| Long-term inflight headroom | 15% | 10% |
| Startup gain / Drain gain | 2.77 / 0.5 | unchanged |
| Probe UP / DOWN gain | 1.25 / 0.90 | unchanged |
| Loss threshold / reduction factor | 2% / 0.7 | unchanged |
| ProbeRTT interval / minimum duration | 5 seconds / 200 ms | unchanged |
| Initial window / cwnd limits / pacing | existing bounded values | unchanged |

The intent is quicker bandwidth rediscovery and somewhat higher utilization, not reproducing the draft's fairness tradeoff. The 10% margin is not removed, loss still ends upward probing, and ongoing loss still reduces the model. ProbeRTT, anti-amplification, cryptography, authentication, packet loss detection and receiver flow control are not disabled.

Reference algorithm: https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html . This fork is a tuned QUIC adaptation, not the Linux TCP module or a claim of formal draft equivalence.

## Lower CPU allocation overhead

The bandwidth sampler previously allocated a sent-state object and a sample object per delivered packet. Sent-state records are now compact inline map values; Get/Remove return independent snapshots and ACK returns a sample by value. ACK takes/removes the record rather than performing an extra lookup followed by removal. Packet-number ordering, duplicate suppression, app-limited sample state and loss/discard semantics remain.

The per-packet record stays below Go's 128-byte inline-map threshold, enforced by a regression. No recycling of borrowed payload buffers, new pool, timer or worker is introduced. Map capacity can remain at a previous peak: this reduces ongoing object churn, not a guarantee of lower idle RSS on every workload.

### Paired local benchmark

Same Apple M4, Go 1.27.1, GOMAXPROCS=1, three 200-ms samples before and after. Each iteration is a complete synthetic flight; times are ns/flight, not ns/packet. No socket or encryption cost is included.

| Workload | Before median ns | Final candidate median ns | Before allocations | Final allocations (reported average) |
| --- | ---: | ---: | ---: | ---: |
| Sampler, 32 packets | 1987 | 1741 | 64 | 0 |
| Full v3 bookkeeping, 32 packets | 4402 | 4295 | 64 | 0 |
| Sampler, 512 packets | 33976 | 28780 | 1024 | 0 |
| Full v3 bookkeeping, 512 packets | 72874 | 69005 | 1024 | 0 |

For 512 packets, sampler time fell about 15.3% and full v3 bookkeeping about 5.3%. The allocation-only intermediate build measured 67849 ns for full v3, but the table uses the final tuned code rather than selecting the best intermediate result.

Final 512-packet full-v3 measurements still averaged about 61–62 B/iteration from amortized map setup/growth. `0 allocs/op` is the benchmark's rounded average, not a claim that a new connection never allocates. Previous full-v3 bookkeeping used about 65560 B/iteration.

Raw before triples (ns): sampler32 [1980,1987,1990]; v3-32 [4402,4395,4547]; sampler512 [33033,33976,34624]; v3-512 [71740,74549,72874].
Final triples: sampler32 [1746,1736,1741]; v3-32 [4282,4305,4295]; sampler512 [28782,28780,28515]; v3-512 [68675,69005,69638].

## Application compatibility

The Tailscale integration at b4f1aa3cd and Tailcat application at f4af70cbe were built with the new QUIC source through external, test-only modfiles. No application source patch was needed. Tailscale wgtransport/quicip/transportprofile tests and targeted authentication, revocation, MSS, batch and close race checks passed. Tailcat full application/CLI/web tests passed. The final serialized Tailcat race suite is recorded in the task evidence, not inferred from a compile.

New checks cover all eight old flag combinations, nil/zero Config, setter compatibility, production controller identity, migration identity, inherited ECN policy, record snapshots, duplicate ACK/loss, reordered ACKs, discard retirement, probe-wait bounds, retained headroom and loss stopping UP. Generic mock-controller/ECN tests explicitly use their mock's wire packet numbering; separate production tests require v3's cross-space sequence.

The library full short suite, focused race suite, go vet and Windows amd64/Linux ARMv7 cross-builds passed. Real loopback duplex/loss/idle and serialized-link loss/idle regressions passed. Existing event-driven FIN acknowledgment and bounded DATAGRAM allocation fixes remain.

## WAN evidence — limited, not an acceptance pass

AU server / US1420 client, normal Tailcat CLI, eight measured seconds plus two explicitly omitted warmup seconds per throughput sample. Both versions use the same application commit and public integration pin. Baseline uses published QUIC v0.63.0-quic.2; candidate includes the preceding DATAGRAM/FIN-ACK work, inline sampling and v3 tuning together. This is not an ablation isolating the two tuning constants.

| Direction | TCP streams | Baseline run 1 Mbps | Candidate run 1 Mbps | Candidate run 2 Mbps |
| --- | ---: | ---: | ---: | ---: |
| US1420 to AU | 1 | 242.64 | 277.66 | 300.01 |
| AU to US1420 | 1 | 247.72 | 265.24 | 252.16 |
| US1420 to AU | 4 | 338.62 | 362.05 | 303.87 |
| AU to US1420 | 4 | 241.34 | 299.65 | 258.99 |

All shown throughput samples completed, with direct H3 evidence, exact sequential/concurrent content checks and idle recovery. Loaded echo errors were zero. Candidate run 1 additionally completed UDP Go API probes at 64/512/1200 bytes, 20 each, all 60 returned. No claim of zero underlying packet loss follows from this.

The forward four-stream candidate range spans below and above the baseline. Reverse loaded p95 was 194.50 ms in the baseline four-stream sample, versus 383.19 / 342.41 ms in the candidate. These small samples are not a universal speed or latency improvement. The reciprocal-order second baseline was attempted but timed out uploading the test executable before any benchmark; no second baseline report/result exists. Do not report this as a completed balanced A/B experiment.

Candidate run 1 printed complete measurements but the outer command timed out at 170 seconds before recording cleanup_errors. A subsequent direct readback found no test processes on either host and unchanged production tailscaled PIDs (AU 498522, US1420 219468). Candidate run 2 completed normally with cleanup_errors=[]. Baseline run 1 also recorded cleanup_errors=[]. The attempted final remote cleanup/readback after the failed second baseline upload was blocked by the tool's safety check and was not retried through an equivalent route. The failed upload directory `/dev/shm/tailcat-pair.4ScCdL1N` was not independently rechecked afterward; no claim of fully verified final cleanup is made.

Whole-run CPU averages from candidate run 1 are not directly comparable because it additionally ran UDP probes. For the matching no-UDP baseline run 1 and candidate run 2, client mean was 35.3% versus 35.4% of one core; server mean 46.6% versus 46.1%. These do not establish a material whole-process CPU reduction. Client peak RSS was 84.3 versus 77.9 MiB and server peak RSS 87.2 versus 84.7 MiB, but one sample does not establish a memory improvement either.

Executable hashes:
- Baseline Linux: 60b515f76dcc7f7d7a5ea02f84de41ea6319bfca22515ccd187c3137151cae00
- Candidate Linux: cc6f68d515e8096b89e4d8afc11eb35815f37926e55b474ce7f82b2ddab4d63c

These are test binaries marked test_only, not new application releases. Private raw files are in the Mac audit directory `tailscale-all/audits/bbrv3-default-20260923`. Do not commit private connection credentials or infrastructure logs into the public repository.

## Remaining release limits

No fresh Tailscale kernel-TUN WAN benchmark, mixed-version WAN matrix, multi-hour stress or fairness study was completed in this task. The change is pushed as a candidate branch only; production daemons, official module pins, GitHub Actions settings, release tags and default branches remain unchanged. The next publication should use a clean public immutable pin and repeat balanced throughput/CPU/loaded-latency measurements before being described as a broadly faster release.
