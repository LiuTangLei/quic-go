# Ready DATAGRAM packing experiment (2026-10-21)

Status: local candidate only. Do not publish on the strength of these results.
No published consumer pin, tag, installed binary or production service was changed.
The candidate code coalesces automatically when this workspace is selected; it
has no runtime feature switch. Tailscale's separate TCP repair remains opt-in and
defaults to false.

The packer previously selected at most one DATAGRAM per QUIC packet. It now
selects ready FIFO frames up to the existing 32-frame queue bound and the current
packet's remaining MTU. It never waits for more data. A frame that fits a fresh
packet stays queued when preceding frames used the remaining room. DATAGRAMs
retain independent boundaries and nil retransmission handlers. Reliable/control
work retains its previous one-DATAGRAM admission opportunity. Encryption, BBRv3,
pacing, congestion accounting and queue capacities are unchanged.

The old control-frame shuffle also included DATAGRAM frames. Coalesced DATAGRAMs
must preserve FIFO order; otherwise packing itself introduces UDP reordering.
Regression tests cover ordering, MTU remainder, an oversized queue head,
reliable/control opportunity, and immutable published transport-parameter reads
in the batch API.

## Local application measurement

Go 1.27.1, Darwin arm64, Mac16,10 (10 logical CPUs). Real inner TCP/UDP sockets in
gVisor use native IP DATAGRAMs over authenticated H3 on loopback. Three TCP echo
connections transfer 16 KiB payloads, every eighth iteration 64 bytes; each
verified echo counts once. UDP offers 250 packets/second of 256 bytes in each
direction. Each window offers load for 5 seconds with a fixed 1.5-second UDP
observation tail. Three trials alternate the TCP repair switch order.

Both executables use the same test fixture: drain at most 32 already-ready IP
packets, without a coalescing timer. The test-only logical vector wrapper does
not advertise kernel GSO. This fixture differs from the earlier single-packet
repair test, so that older record is not the causal baseline here.

The unshaped comparison interleaves old/new, new/old, old/new executable runs and
uses no CPU profiler. Default TCP repair is off:

| Workload | Before Mbps | Candidate Mbps | Change |
| --- | ---: | ---: | ---: |
| Unshaped, medians | 201.833 | 203.773 | +0.96% |
| burst, three-window means | 8.227 | 8.437 | +2.55% |
| reorder, three-window means | 5.561 | 5.386 | -3.15% |
| asymmetric, three-window means | 0.455 | 0.516 | +13.45% |

All six unshaped mode windows per executable deliver all 15,000 offered UDP
packets with no duplicates or reordering. Maximum per-window unconditional P99
is 1.628 ms before and 1.588 ms in the candidate. With experimental TCP repair
on, unshaped medians are 196.901 -> 199.210 Mbps (+1.17%). These are small local
application gains, not Internet throughput or whole-process CPU claims.

Impairment uses the existing three scenarios and unchanged bounded shaper:
1,500,000 bytes/s and 8 ms one-way delay for burst/reorder; 750,000/150,000
bytes/s and 6/22 ms one-way delays for asymmetric. Burst drops three consecutive
outer packets every 1009; reordering delays every 37th packet by another 24 ms.
The asymmetric case includes both impairments. Both code variants complete all
18 functional windows, but both parent tests exit nonzero at the UDP gate.
Packet grouping changes which inner packets see a given outer impairment, so
the runs have identical parameters, not identical packet loss traces.

Keep the existing gate: loss delta <=0.5 percentage points, no duplicates, and
finite unconditional P99 <=max(baseline*1.10, baseline+5 ms). TCP repair on/off
fails 11 directional comparisons in each variant. The additional before/after
packing comparison fails 20 of 36 directional comparisons: 19 involve a missing
finite unconditional P99, one exceeds the loss delta and one finite P99
regresses (one comparison has two reasons). No impairment window has duplicate
UDP delivery. Do not count delivered-only P99 as unconditional P99 or weaken the
gate because the old runtime also loses more than 1% of UDP in some scenarios.

Root QUIC, HTTP3, handshake and selected Tailscale transport/quicip race suites
pass. The checks do not cover every consumer/platform or a real TUN/WAN link.
Baseline QUIC source is 68e8f0ee; application baseline is a3e060769. Tests use the
local Go workspace, not release module pins.

## Evidence and reproduction

The local audit root is `/Users/lei/code/tailscale-all/audits/h3-braidpath-20261021`.
`h3-throughput-manifest.json` freezes source/binary hashes, commands and outcomes;
`h3-throughput-analysis.json` contains every directional gate failure and raw
aggregate. `run-h3-throughput-compare.py`, `run-h3-impairment-compare.py` and
`summarize-h3-throughput.py` reproduce the serial measurements from saved test
executables. The preliminary profiled candidate with the old frame shuffle is
excluded. The valid profiled runs are diagnostic only, not the primary table.

Build a measurement executable with `go test -c ./wgengine/wgtransport/quicbind`
from the application repository in the configured workspace. Select
`TestH3NativeThroughputLoad` with `TS_H3_THROUGHPUT=1`, or
`TestTCPRepairNativeMixedLoad` with `TS_H3_REPAIR_LOAD=1`; set
`TS_H3_REPAIR_SECONDS=5`, `TS_H3_REPAIR_TRIALS=3` and `TS_H3_REPAIR_RESULT` to an
absolute JSON output path. Measure executables serially without `-race` or a
profiler. Preserve failing gate results instead of rerunning to select a pass.
