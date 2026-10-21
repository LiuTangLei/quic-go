# Native H3 IP bulk measurement and allocation experiment (2026-10-21)

Status: experimental, not qualified for release. UDP nonregression fails in the
impairment matrix, and the bulk throughput gain is not direction-independent.
No published pin, installed service, network route or host setting was changed.
The candidate code is active only when this isolated local workspace is selected;
there is no runtime switch for these allocation changes. TCP repair remains off
by default. No FEC was added.

## Correct measurement before optimization

The previous mixed-load receive bridge called `receiveTestSlots` on every packet.
That convenience helper allocates approximately 128 KiB each time. The benchmark
now reuses one receive slab per pump and drains already-ready vectors without
waiting. This is a test correction, not a production throughput improvement.
The older approximately 200 Mbps echo result is not a single-direction ceiling.

The new opt-in `TestH3NativeOneGiBBulk` sends exactly 1,073,741,824 bytes over one
inner TCP connection, continuously, without per-chunk echo, parallel bulk, Range
or retry. Each direction gets a fresh authenticated H3 session and two gVisor
stacks over the host Bind adapter with real loopback UDP. Each input/read slab
is reused. A 64 KiB deterministic source buffer bounds generation memory;
received SHA-256 must match `4554560acd63e0374cc9ae3d5da8f603a3b299784d288b5927f75254b6a22287`.
The receiver hashes incrementally and does not retain the entire file.

The load shape and one-GiB size match a single-stream bulk download. The topology
is not BraidPath's historical WAN/SOCKS/file-download test. The timer starts after
TCP/H3 establishment and expected-hash preparation, and stops after receiving
and hashing the last byte. It excludes cold connection time and disk persistence.
It is not a public-network, real-TUN, kernel-TCP or full production magicsock test.
Do not compare the absolute Mbps causally with BraidPath's 502.9 Mbps WAN record.

Both executables contain the same corrected fixture. Three unprofiled trials
run serially in baseline/candidate, candidate/baseline, baseline/candidate order.
Each executable transfers one GiB in each direction: twelve GiB primary total.
Go 1.27.1, Darwin arm64, Mac16,10, ten logical CPUs. No builds, other performance
checks or profilers run concurrently with the timed comparisons.

## Results

| Direction | Baseline median Mbps | Candidate median Mbps | Change | Allocation change |
| --- | ---: | ---: | ---: | ---: |
| 0->1 | 900.38 | 906.55 | +0.69% | -31.54% |
| 1->0 | 919.58 | 866.17 | -5.81% | -30.99% |

All twelve files complete with the expected length and SHA-256. Baseline ranges
815.68–949.58 Mbps; candidate ranges 794.13–1006.08 Mbps. Allocation means total
process allocation during the transfer, not resident memory or whole-process CPU.
Median allocation is about 5.15–5.19 GB before and 3.55 GB in the candidate.

Each transfer carries 250 UDP packets/s of 256 bytes in each direction, with a
fixed 1.5-second receive tail after bulk completion. Across the six bulk windows
per variant, baseline delivers 28,515/28,525 and candidate 29,033/29,038 packets.
Neither variant produces UDP duplicates or reordering. All twelve paired UDP
directional comparisons pass the existing relative loss/unconditional-P99 gate.
Different transfer times produce different offered populations; compare loss
fractions and complete-population quantiles, not raw missing counts alone.

## Located costs and changes

CPU sampling in both variants concentrates on UDP system calls and runtime
scheduling. The portable host batch is not kernel GSO; on Darwin the selected
StdNetBind still writes outer datagrams separately. This identifies a local
processing hot path, not a proven WAN bottleneck or CPU saturation diagnosis.
No claim of a BBR/window limit or a universal CPU gain follows from the profile.

The allocation profile also identified two concrete redundant costs:

- Receive: the synchronous frame parser allocated/copied each DATAGRAM before
  the direct callback copied it into the bounded application queue. The candidate
  borrows a capacity-limited slice from the authenticated decrypted packet until
  handling returns. Async/default/fallthrough queues still copy, as required for
  ownership. Qlog stores only the length. Standalone parsing keeps owned data.
- Send: an interface call made the temporary 32-payload descriptor array escape
  on every batch. Reuse peer-local descriptors under the existing send mutex,
  clear references on every success/error path, reuse the context prefix and
  keep exceptional error work off the ordinary success path.

The candidate profile removes the roughly 1.1 GB receive payload-copy hotspot
and approximately 0.3 GB temporary send-descriptor hotspot per profiled GiB.
Remaining large allocations include owned send data and the test bridge's gVisor
packet copies. Authentication, live policy checks, MTU, congestion, pacing,
DATAGRAM boundaries, FIFO and bounded queues are unchanged.

## Impairment acceptance remains failed

Keep the existing scenarios, five-second loads, three trials and alternating
TCP-repair switch order. Run the complete baseline matrix then the complete
candidate matrix, serially, using the corrected fixture. All 36 functional
windows complete, but both parent tests exit nonzero at the UDP gate.

New/old candidate comparison fails 20/36 UDP directional checks: 18 involve a
non-finite unconditional P99; two have finite P99 regression, and two exceed the
loss delta (some reasons overlap). With repair off, burst trial 1 direction 0
changes unconditional P99 37.877 -> 47.536 ms; asymmetric trial 1 direction 0
changes 43.448 -> 113.096 ms. With repair on, two reorder comparisons increase
missing packets by seven per approximately 1250 offered packets. These samples
do not isolate which change caused the difference, but they do block promotion.

Repair-on/off gates fail 9/18 directional checks before and 10/18 after. Keep
loss delta <=0.5 percentage points, no duplicates, and finite unconditional P99
<=max(baseline*1.10, baseline+5 ms). Missing packets stay in the population; do
not replace unconditional P99 with delivered-only P99 or relax a gate because
the baseline also fails. Outer grouping and host timing mean identical impairment
parameters do not produce identical per-inner-packet impairment traces.

## Checks and evidence

QUIC root, wire parser, HTTP3, handshake, selected Tailscale transport/quicip
race tests, focused scratch ownership tests, one-MiB mixed UDP bulk race smoke,
and client/daemon native builds pass. These are not every platform/consumer.
The source application baseline is af25bb819; QUIC baseline is a369f536.

Audit root: `/Users/lei/code/tailscale-all/audits/h3-braidpath-20261021`.
`native-bulk-manifest.json` records source/executable hashes, commands and outcomes.
`native-bulk-analysis.json` lists each directional result and failure.
`run-native-bulk-compare.py`, `run-native-bulk-impairment.py` and
`summarize-native-bulk.py` preserve the fixed order and raw observations. Profiled
runs and one-MiB race smoke are separate diagnostics, not primary throughput data.

Reproduce by compiling the opt-in test executable and running
`TestH3NativeOneGiBBulk` with `TS_H3_BULK=1` and an absolute
`TS_H3_BULK_RESULT` JSON path. Default size is one GiB per direction;
`TS_H3_BULK_BYTES` only allows a smaller smoke-test size. Run the saved executables
serially without profiler or race flags for performance comparisons. No completed
workload was repeated to select a more favorable result.
