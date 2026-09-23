# Full validation: final supplemental evidence (2026-09-24)

## Decision

**Do not publish the optimized runtime yet.** Functional, compatibility and platform checks listed below pass, but the capacity-drop and 5%-loss stress cases fail. They also fail with the published fork explicitly using its BBRv3 controller. Fresh WAN throughput, equal-load process CPU and full application mixed-version network acceptance are not completed.

Runtime source: `4600d8a2f91078d2ae503740336f56bab5104a14`.
Baseline: `v0.63.0-quic.2` / `1242e7347f2d15a6fd0b5b8ccbb6e7a5318f4608`.
Fixture repair: `a1af8f4172a99be948a31a0f770714262c849f64`.
The fixture commit changes only three `_test.go` files. A final diff against the runtime source, excluding tests and documentation, is empty. Application module files remain unchanged.

This supplements the earlier checkpoint in `FULL_TEST_RESULTS_20260924.md`. In particular, explicit successful local Linux and raw-QUIC loopback records below now exist; they do not turn the blocked WAN or full mixed-application checks into passes. All original failed logs remain in the audit directories.

## Final observed checks

Counts are Go test/subtest events, not unique production workflows.

| Check | Outcome | Evidence |
| --- | --- | --- |
| Complete ordinary QUIC tree, Go 1.26.0 | 2701 pass, 4 skip | `quic-full-go126-final.json` |
| Complete race-instrumented QUIC tree, Go 1.27.1 | 2697 pass, 4 skip | `quic-full-race-causal-v1.json` |
| HTTP3 shutdown fixtures, 30 race repetitions | 120 pass events | `shutdown-causal-race-v1.json` |
| Tailcat full application/CLI/web race suite | 266 pass, 4 skip | `tailcat-full-race-final.json` |
| Tailscale selected integration race suites | 657 pass, 3 skip | `tailscale-integration-race-final.json` |
| Native Linux arm64 QUIC root-package tests in isolated local container | 799 pass, 1 opt-in skip | `linux-isolated-core-capability-final2.log` |
| QUIC v2 self integration | pass | `quic-v2-full-1790180537646240000.json` |
| Raw QUIC old/new loopback | 14 complete passing runs | `mixed-version-loopback-final.json` and its log |
| Tailcat CLI cross-builds | 11 OS/architecture combinations pass | `build-*.json` in the second audit directory |
| Browser js/wasm output build | pass | `browser-wasm-explicit-output-final.json` |

Tailscale's selected integration set is `wgengine/wgtransport/...`, `wgengine/quicip`, `wgengine/transportprofile`, `wgengine/magicsock`, `net/tstun`, and `tsnet`. It is not every package or user workflow in the Tailscale monorepo. Both consumers use test-only external modfiles pointing to the candidate QUIC source. Tailcat is `f4af70cbe`; the Tailscale integration is `b4f1aa3cd`.

The four macOS QUIC skips are the opt-in long-transfer experiment plus three Linux GSO/sendmsg tests. Long transfers were run separately. The three Linux-specific tests, as well as forced socket-buffer tests, pass in the local Linux run. Linux's only root-package skip is the same opt-in long-transfer experiment. Consumer-specific skips remain recorded in their JSON files and must not be silently counted as executed.

### Independent local Linux environment

The local Docker endpoint was a Unix socket on the Mac, not a remote test host. Tests ran from the compiled Linux arm64 root-package test binary in the existing Alpine image `sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce`.

The container had no external network (`--network none`), read-only source and root filesystem, a temporary `/tmp`, two CPUs, 1 GiB memory and a PID limit. All capabilities were dropped, then only `NET_ADMIN` was added for the two tests that explicitly force sizes on their own UDP sockets. No host networking, privileged container, persistent service, route or host sysctl was used. The named container was removed after exit.

An initial run failed because the test certificate helper uses a compile-time source path; the source was then mounted read-only at that same path. The next run failed only the two force-buffer tests with EPERM because root without the required capability does not satisfy their assumption. The final setup completed the whole root package. These failures are retained as environment/setup failures, not hidden or fixed by changing production code.

### Loopback interoperability scope

The recorded 14-case helper runs use separate old/new executables, with both endpoint directions and old default/BBRv1/BBRv3 policies. Each complete case checks four concurrent reliable streams, 2 MiB of echo accounting and 32 DATAGRAM messages. New endpoint statistics must report `bbr-v3`.

This is local raw-QUIC protocol compatibility with pinned test TLS, not a claim to have mixed old and new installed Tailscale/Tailcat application nodes over the Internet. The code and existing consumer tests still enforce their separate application authentication contracts. A later source-guarded relaunch refused to start when only the fixture/documentation commit advanced; that refusal is not a new test failure or a pass.

## Fuzz and cross-platform coverage

Four additional short fuzz jobs (eight-second requested budgets, two workers) completed without a reported failure:

- HTTP3 frame parser: 22924 executions.
- Transport parameters: 60144 executions.
- Stream frame sorter/reassembly: 200367 executions.
- TLS handshake input: 74917 executions.

Total: 358352 executions. These bounded runs are not an exhaustive security or correctness proof. Earlier shorter fuzz runs remain separate evidence and are not added to these counts.

The 11 CLI cross-builds cover Linux amd64/arm64/ARMv7; macOS amd64/arm64; Windows amd64/arm64; FreeBSD amd64/arm64; OpenBSD amd64/arm64. WASM was built separately. Its first command omitted `-o` and collided with the existing `web` source directory; the explicit output-path build passed. Neither cross-building nor WASM compilation is native Windows/BSD/Android/browser runtime verification.

## Virtual-link stress results

Eight completed scenarios each verified 200 MiB using SHA-256, for 1600 MiB total. These are locally simulated links with virtual time, 60 ms RTT and Chromium-inspired handshakes, not WAN or wall-clock throughput measurements. Link-rate accounting covers QUIC UDP payload bytes, excluding outer IP/UDP/link headers.

| Scenario | Forward goodput | Reverse goodput |
| --- | ---: | ---: |
| 20 Mbps, 4-BDP queue | 19.12 Mbps | 19.18 Mbps |
| 20 Mbps, 0.25-BDP queue | 19.00 Mbps | 19.13 Mbps |
| 20 Mbps, seeded 1% packet loss | 17.94 Mbps | 17.95 Mbps |

The forward 20-to-100 Mbps step at virtual second 20 reaches three consecutive full samples above 90% of the new configured rate after about two seconds. Whole-transfer average is 49.33 Mbps. The reverse control changes the opposite link, not its bulk-data bottleneck, and therefore is not a reverse 100 Mbps recovery result.

### Blocker 1: capacity falls from 100 to 20 Mbps

Setup: 200 MiB, 60 ms RTT, drop at virtual second 5, original 3,000,000-byte queue limit retained, seed 42. Both baseline BBRv3 and new default BBRv3 abort with `INTERNAL_ERROR: too many gaps in received data`.

In the paired control, baseline aborts at about 18.12 virtual seconds and candidate at about 31.58 seconds. Both are incomplete transfers. Their partial goodput must not be represented as successful throughput or used to claim one version faster. The earlier candidate failure at about 24.28 seconds is also retained.

### Blocker 2: seeded 5% random loss

Setup: 200 MiB, 20 Mbps, 60 ms RTT, seed 97. Both builds miss the approximately 533-second virtual deadline. Partial received goodput is about 2.294 Mbps for baseline BBRv3 and 2.290 Mbps for the candidate; neither provides a complete 200 MiB hash result.

These failures are inherited or shared with the existing BBRv3 implementation. That is not a release waiver, proof of the exact historical SG/US cause, or a complete root-cause diagnosis. No gap limit, queue bound, encryption check or timeout was weakened to turn these stress failures into passes.

Paired raw evidence: `stress-controls-1790180277972927000.json`, plus the four `old-v3-*` / `new-default-*` sample and log files in `audits/quic-full-20260923`.

## Test repairs, source and operational status

The long fixture now checks the actual default `bbr-v3` instead of a deprecated BBRv1 request bit. The stream-parser zero-allocation assertion remains under the ordinary runtime; race-mode frame-content checks remain active. The race runtime intentionally drops some sync.Pool entries, so its allocation behavior is not an ordinary zero-allocation benchmark. Primary reference: https://go.dev/src/sync/pool.go.

The HTTP3 close fixture now synchronizes on handler entry and compares completion with the actual shutdown deadline, rather than charging handshake/scheduling time to a 25 ms shutdown window. Its ticker is stopped. Early close, failure to unblock and wrong error causes still fail the tests.

A remote-node preflight was blocked by the execution tool before host commands ran. It was not rerouted or retried. Independent localhost and isolated Linux tests do not constitute the missing WAN test. No new equal-load process CPU saving or real Internet Mbps result is claimed here.

The default branch runtime and published `v0.63.0-quic.2` tag are not changed by these tests. No production binary was installed, no application pin was updated, no release was created and the library's GitHub automation was not enabled.

Audit roots on the Mac:
- `/Users/lei/code/tailscale-all/audits/quic-full-20260923/`
- `/Users/lei/code/tailscale-all/audits/bbrv3-full-20260924/`
- The loopback helper retains its run history under `audits/bbrv3-default-20260923/loopback-interop/`.
