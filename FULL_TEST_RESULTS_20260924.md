# Full validation of the tuned BBRv3 candidate

> This is an earlier validation checkpoint. See
> [the final supplement](FULL_TEST_SUPPLEMENT_20260924.md) for subsequently
> completed local Linux GSO/socket tests, recorded raw-QUIC loopback results,
> consumer test counts, cross-builds and fuzz coverage. The stress failures and
> missing WAN acceptance below remain release blockers.

## Verdict

**Not release-ready.** The ordinary full suite and final serialized full race
suite pass after test-fixture repairs, and both application consumers pass their
listed checks. Additional stress cases expose two failures also reproducible on
the published BBRv3 baseline. Fresh WAN throughput/CPU acceptance remains missing.

Runtime under test: `4600d8a2f91078d2ae503740336f56bab5104a14`.
Published comparison: `v0.63.0-quic.2`, `1242e7347f2d15a6fd0b5b8ccbb6e7a5318f4608`,
explicitly selecting its existing BBRv3 controller for the stress control.
Test-fixture commit: `a1af8f4172a99be948a31a0f770714262c849f64`.
No production runtime file was changed during this validation. No new release,
application dependency pin, or production service rollout is part of this work.

## Completed validation

Go 1.27.1 on macOS arm64:

- Full, non-short QUIC suite: 2697 passed test/subtest events, four skipped.
- Final serialized full race suite: the same 2697 passed events and four skipped;
  no failed test. Earlier failing runs are retained, not relabeled successful.
- The opt-in long test was run separately. The other three skipped tests exercise
  Linux-specific GSO/sendmsg behavior and were not natively executed on macOS.
- Tailcat at `f4af70cbe`, with the candidate library through an external modfile:
  full ordinary and serialized race suites passed.
- Tailscale integration at `b4f1aa3cd`: `wgengine/...`, `net/tstun`, and `tsnet`
  passed; race checks for `wgtransport/...`, `quicip`, and `transportprofile`
  passed. This is not a claim to have run every package in the Tailscale monorepo.
- Five-second fuzz runs with two workers: QUIC wire frames 198857 executions,
  HTTP/3 frame parsing 32500, frame sorting/reassembly 120682; all passed.
  These short fuzz runs are not exhaustive proofs.
- The QUIC tree cross-built for 11 targets: Linux amd64/arm64/ARMv7,
  macOS amd64/arm64, Windows amd64/arm64, FreeBSD amd64/arm64, and
  OpenBSD amd64/arm64. Cross-building is not native device execution.

## Completed baseline stress matrix

Eight opt-in long transfers each sent and verified 200 MiB. These use the local
virtual network, not the Internet or a measurement of native UDP syscall cost.
The link's configured rate accounts for QUIC UDP payload bytes, not all link
headers. RTT is 60 ms; the handshake profile is Chromium-inspired.

| Condition | Forward application goodput | Reverse application goodput |
| --- | ---: | ---: |
| 20 Mbps, 4-BDP queue | 19.12 Mbps | 19.18 Mbps |
| 20 Mbps, 0.25-BDP queue | 19.00 Mbps | 19.13 Mbps |
| 20 Mbps, seeded 1% random loss | 17.94 Mbps | 17.95 Mbps |

The forward bandwidth-increase case changes 20 to 100 Mbps at virtual second 20;
three consecutive full samples reach at least 90% of the new rate after about
2 seconds. Its whole-transfer average is 49.33 Mbps, not 100 Mbps. The reverse
control does not undergo a bulk-data bandwidth increase: that test changes only
the forward link. All eight scenarios pass length and SHA-256 checks.

## Stress failures that block acceptance

### Sudden capacity drop: 100 to 20 Mbps

A 200 MiB forward transfer, 60 ms RTT, with bandwidth reduced at second 5 and the
original queue byte limit retained, aborts with:

```
INTERNAL_ERROR: too many gaps in received data
```

The first candidate run aborts after approximately 24.28 virtual seconds. A
separate control repeats the same failure with published BBRv3 (approximately
18.12 seconds) and the candidate (approximately 31.58 seconds). Both report the
receiver gap limit, rather than a successful final byte/hash check. These are
partial transfers, so their average rates must not be compared as successful
benchmark scores. Probe jitter and scheduling also differ between runs.

### Seeded 5% random packet loss

At 20 Mbps and 60 ms RTT, seed 97, the 200 MiB transfer does not complete before
its approximately 533-second virtual deadline. The initial candidate sample
averages approximately 2.27 Mbps. In the controlled repeat, old BBRv3 averages
2.294 Mbps and new default BBRv3 averages 2.290 Mbps; both time out without a
complete 200 MiB integrity result.

This reproduces existing BBRv3 stress weaknesses, not proof that this optimization
introduced them. It also does not establish the cause of any historical WAN
instability. No receive-gap limit was enlarged, queue bound removed, loss response
disabled, or timeout relaxed to make these scenarios pass.

## Test-fixture repairs

Only three `_test.go` files were changed:

1. The old long-transfer helper asserted the deprecated BBRv1 request bit. It now
   uses the default configuration and checks the live `bbr-v3` controller on both
   endpoints, allowing the intended stress test to run.
2. The stream-parser allocation fixture required zero allocations under the race
   runtime, which intentionally drops some sync.Pool entries. Frame-content
   checks still run under race instrumentation; zero-allocation assertions are
   retained under the ordinary runtime. This is not a runtime pool change.
3. HTTP/3 graceful-close fixtures included connection/handshake scheduling in a
   25 ms shutdown timing assertion, and started shutdown before proving that the
   request handler was entered. They now check the actual shutdown deadline and
   use a handler-entry signal. A previously unstopped ticker is stopped. Early
   completion and failure to unblock are still errors.

The allocation and graceful-close failures were reproduced on the published
baseline. Earlier full race runs and their errors remain in the audit. The final
passing serial run does not prove the absence of all scheduling-sensitive bugs.

Primary explanation of race-mode pool behavior:
https://go.dev/src/sync/pool.go

## Uncompleted checks and operational boundaries

The fresh AU/US Tailcat baseline WAN launch was blocked by the execution tool
before launch. It was not repeated through another runner or agent. A fresh
14-case old/new raw-QUIC loopback helper was also blocked; historical results
from an earlier task are not substituted for this run.

The workspace connector subsequently returned 404 for local audit reads; the
available Mac connector was used to read records and continue independent local
Go suites. It was not used to rerun either blocked operation.

Therefore no new Internet Mbps, equal-load process CPU reduction, fresh complete
mixed-version application matrix, Linux-native GSO result, multi-hour soak,
Windows/Android native runtime result, or release approval is claimed.

Test builds are explicitly non-release artifacts. The production daemons,
installed client binaries, published tags, and library Actions settings were not
changed. The next performance work should investigate bounded loss/retransmission
and reassembly recovery under capacity drops and higher random loss before
further increasing probing aggressiveness.
