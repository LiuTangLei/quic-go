# Tuned BBRv3 full validation — 2026-09-24

## Decision: release acceptance NOT passed

Runtime under test: `4600d8a2f91078d2ae503740336f56bab5104a14`.
Reference: published `v0.63.0-quic.2`, commit
`1242e7347f2d15a6fd0b5b8ccbb6e7a5318f4608`.
Toolchain: Go 1.27.1, macOS arm64 / Apple M4.

The normal suites and final serialized full race suite pass, as do both consumer
suites. A bandwidth-collapse stress scenario, however, aborts an incomplete
reliable stream with `too many gaps in received data` in both old BBRv3 and the
new tuned runtime. The new runtime reproduced that failure in a separate run.
Do not publish a new library/application release on the strength of the passing
unit tests alone. WAN performance and native remote-platform execution were not
completed: the execution tool blocked the remote inventory command. It was not
retried via an equivalent alternate route.

## Reproducible source boundary

The initial workspace had existing test edits and changed during inspection.
Those edits were left alone. Acceptance tests use a separate worktree fixed at
4600d8a2, with only these two test-fixture corrections:

1. The opt-in long transfer checks the actual `bbr-v3` controller, rather than
   a legacy EnableBBR request bit that Config normalization now clears.
2. The STREAM parsing zero-allocation assertion is checked in the ordinary
   runtime. Under the race runtime, all parsing checks still run, but sync.Pool
   allocations are diagnostic: Go deliberately discards some pool entries in
   that mode. No parsing assertion or receive safety limit is removed.

No runtime Go implementation, application go.mod, existing tag, production
binary, node identity or Actions configuration is changed by this test work.
The individual records include the source revision and before/after diff hash.

## Completed checks

| Area | Result |
| --- | --- |
| Entire QUIC tree, non-short `go test ./...` | Pass |
| Entire QUIC tree, serialized non-short race tests | Final run passes; earlier failures retained below |
| QUIC v2 self-integration suite | Pass |
| Tailscale wgtransport, quicip, transportprofile, magicsock, tstun, tsnet | Pass |
| Tailscale transport integration race suites | Pass |
| Complete Tailcat application/CLI/web suite | Pass |
| Complete serialized Tailcat race suite | Pass |
| Fixed-seed roughly one-third-loss handshake regression | 48 subcase executions pass under race |
| Old/new real-socket loopback interoperability | Final 14/14 pass after fixture receipt fix |
| Four short fuzz campaigns | 358,352 executions; no failing input reported |
| Go vet | Pass |
| Tailcat native-target cross-builds with new library | 11 OS/architecture combinations pass |
| Tailcat browser js/wasm compilation | Pass with explicit output filename |

Tailscale consumer source is b4f1aa3cd3001425ab263c53250d93af14dbb1b6.
Tailcat source is f4af70cbe74922e1fe4daa55db435d1dc9ec41d1. External test-only
modfiles select the candidate QUIC worktree; the application source trees remain
unchanged. Mac arm64 and Linux amd64 test binaries were also built for both
consumers. They are not published replacements for an existing release.

Cross-builds: Linux amd64/arm64/armv7; macOS amd64/arm64; Windows amd64/arm64;
FreeBSD amd64/arm64; OpenBSD amd64/arm64. This does not establish native execution
on non-Mac operating systems or runtime browser compatibility.

## Long-transfer virtual-link matrix

The old and new versions use an identical overlaid fixture, explicitly selecting
BBRv3 on the old version and checking the real controller on both. All cases use
Chromium-inspired ClientHello, bounded queues and a fixed simulated link. Values
are virtual-link goodput, NOT measured Internet or host-socket throughput.

| Case / direction | Payload per build | Old BBRv3 Mbps | Tuned BBRv3 Mbps | Integrity/completion |
| --- | ---: | ---: | ---: | --- |
| Deep queue, forward | 128 MiB | 19.089 | 19.080 | Both pass |
| Deep queue, reverse | 128 MiB | 19.056 | 19.155 | Both pass |
| Shallow queue, forward | 64 MiB | 18.948 | 19.033 | Both pass |
| Shallow queue, reverse | 64 MiB | 19.091 | 19.059 | Both pass |
| Seeded 1% loss, forward | 64 MiB | 17.929 | 17.561 | Both pass |
| Seeded 1% loss, reverse | 64 MiB | 17.601 | 17.791 | Both pass |
| Bandwidth 20 -> 100 Mbps | 128 MiB | 54.672 | 54.712 | Both pass |
| Bandwidth 100 -> 20 Mbps | 128 MiB | Not a valid completed-transfer result | Not a valid completed-transfer result | Both FAIL |

Overall: 14/16 long-transfer cases pass, with 1,280 MiB (1.25 GiB) of completed
one-way payload checked by SHA-256. Failed partial transfers are not included in
that count and their apparent rate is not presented as successful throughput.
These fixed-capacity cases do not demonstrate a universal speed gain.

### Bandwidth drop failure details

The forward path starts at 100 Mbps with a 60 ms RTT and changes to 20 Mbps at
five virtual seconds. Reverse capacity is 100 Mbps. Queue storage remains at
four initial BDPs (~3,000,000 bytes); it is not resized at the rate change. Stream
receive credit is 32 MiB and connection credit 64 MiB. The intended file is
128 MiB. This is a stressful simulated deep-queue scenario, not an observation
that a particular Internet path suffered the same fault.

| Build/run | Received bytes | Approx. received MiB | Virtual elapsed | Result |
| --- | ---: | ---: | ---: | --- |
| Published v3 reference | 87,480,059 | 83.43 | 18.10 s | Local/remote INTERNAL_ERROR: too many gaps |
| Tuned candidate | 122,445,276 | 116.77 | 32.47 s | Same error |
| Separate candidate recheck | 110,653,541 | 105.53 | 30.65 s | Same error |

The recheck uses the same network parameters; packetization/probing schedules
are not claimed byte-identical. The receive-side frame sorter has a 1,000-gap
safety boundary. Keep that protection. The failure needs investigation of
bandwidth-loss recovery, retransmission progress and reorder accumulation; these
results do not prove which component is the root cause. Hashes differ because
the transfers ended early, not evidence of a cryptographic integrity failure.

Reproduce on this test branch, locally and without changing the host network:

```sh
GOTOOLCHAIN=go1.27.1 GOMAXPROCS=2 \
BBR_LONG_TEST=1 BBR_LONG_MODE=virtual BBR_LONG_CASE=step \
BBR_LONG_MBPS=100 BBR_LONG_REVERSE_MBPS=100 BBR_LONG_RTT_MS=60 \
BBR_LONG_STEP_SECONDS=5 BBR_LONG_STEP_MBPS=20 BBR_LONG_MIB=128 \
BBR_LONG_PROFILE=chromium-h3 BBR_LONG_SEED=42 \
BBR_LONG_OUTPUT=/tmp/quic-step-down.jsonl \
go test . -run '^TestBBRLongTransfer$' -count=1 -timeout=90s -v
```

## Interoperability and fuzz detail

Each interoperability case verifies four concurrent streams containing 16 MiB
in each direction after echo, plus 32 independent DATAGRAM echoes. New/new and
both old/new role assignments are tested twice. The old side uses original
default, legacy BBRv1 and legacy BBRv3 policies; every new endpoint must report
bbr-v3. Final total: 14 cases and 448 DATAGRAM echoes. Socket addresses are
strictly loopback; this is not WAN or application identity-policy validation.

Fuzz totals: HTTP/3 frame parser 22,924; transport parameters 60,144; frame sorter
200,367; handshake input 74,917. Campaign budgets were eight seconds each, with
two workers. This is limited fuzz coverage, not a proof of absence of bugs.

## Failures and harness corrections retained

- The first parallel full race run hit two one-second cancellation timeouts and
  a graceful-shutdown timing assertion. In five targeted repetitions, cancellation
  cases passed; one candidate graceful-timing case failed while the old reference
  group passed. Later serialized full runs passed the original timing tests.
  No runtime fix or proof of the exact timing root cause is claimed.
- A clean full race run reproduced the inherited sync.Pool zero-allocation
  assertion failure. The test-only mode distinction described above was then
  validated with ten ordinary and ten race-mode repetitions. Ordinary mode still
  requires zero allocations.
- Initial expanded interoperability was 13/14. The client closed after its receipt
  was transport-ACKed, before the server application necessarily read it. The
  fixture now requires a server application-consumption confirmation before
  closing the connection. Payload checks, ACK waits and error handling remain.
  The corrected fixture passes 14/14; QUIC runtime code was not changed for this.
- An initially misspelled seeded-test regexp matched no tests. It is NOT counted
  as a pass; the exact real test name was rerun with 48 executed subcases. The
  evidence collector now treats 'no tests to run' as an invalid test result.
- The first WASM build omitted -o and collided with the existing web directory.
  The corrected explicit output path builds successfully; this was a command
  issue rather than a compiler or runtime defect.

The raw unsuccessful records remain unchanged. A normalized summary separates
invalid invocations, fixed harness issues, passing checks and unresolved failures.

## Outstanding acceptance

Real WAN old/new throughput, equal-offered-load process CPU, loaded tail latency,
remote native-platform runtime, real browser execution and multi-hour stress are
not completed in this run. Earlier WAN results from different builds are not
reused. The current bandwidth-drop failure is itself sufficient to withhold
release acceptance until fixed and revalidated.

Private audit records: `tailscale-all/audits/bbrv3-full-20260924/` on the maintainer
Mac. They include frozen fixtures, command logs/hashes, external modfiles,
long-matrix JSONL, interop results, build identities and normalized SUMMARY.json.

Primary Go runtime reference for race-mode pool behavior:
https://github.com/golang/go/blob/master/src/sync/pool.go
