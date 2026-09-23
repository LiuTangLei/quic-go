# Shared QUIC fork CPU and compatibility review — 2026-09-23

## Scope

Reviewed source: `v0.63.0-quic.2`, commit
`1242e7347f2d15a6fd0b5b8ccbb6e7a5318f4608`. Work is isolated on
`perf/shared-quic-cpu-20260923`. This is a candidate, not a new release or a
production rollout. No application dependency pin or GitHub automation is changed.

The consumers have distinct data paths. Tailscale's native H3 IP engine sends
inner IP packets through CONNECT-IP / QUIC DATAGRAM. Tailcat's TCP proxy uses
reliable HTTP/3 DATA streams; its UDP/IP services use DATAGRAM. They share the
QUIC packet protection, congestion/pacing, stream and datagram implementations.
The earlier MSS/TUN batching and Tailcat read-buffer work lives in the consuming
integration, not in this library patch. Those earlier speed gains must not be
attributed to this work.

## Existing code worth retaining

- `datagram_send_batch.go`: the optional public batch API validates up to 128
  inputs before enqueueing, uses the existing 32-frame send queue, reports the
  accepted prefix on failure, and wakes for available groups rather than every
  packet. It does not wait to form a batch.
- `datagram_receive_handler.go`: optional synchronous borrowed-byte dispatch;
  consumers must not block or retain borrowed storage. Returning false retains
  normal ReceiveDatagram behavior.
- `datagram_queue.go`: receive bounds are 1024 datagrams / 2 MiB with local-drop
  accounting. Increasing these limits is not a fix for sustained overload.
- `packet_batch.go` and `send_queue_batch.go`: preserve protected UDP packet
  boundaries, same-destination batching, error handling and exactly-once buffer
  release. GSO/OOB/ECN-capable paths retain their separate semantics.
- `http3/datagram_batch.go`: preserves HTTP/3 stream association and prefix
  framing while using the QUIC batch API.

In particular, PacketBatchWriter explicitly does NOT advertise DF, ECN or GSO.
An abstract magicsock PacketConn is not interchangeable with a native UDP socket
exposing those capabilities. Native GSO support cannot safely be enabled merely
by asserting a flag in this adapter.

## Change 1: bounded group allocation for DATAGRAM sends

Previously, each accepted input in a batch allocated a DatagramFrame and a
prefix+payload byte slice. A group of 32 inputs therefore produced 64 allocations.

The candidate allocates one frame array and one byte slab for each group of at
most 32 inputs. Every DatagramFrame retains a separate slice whose length AND
capacity end at its packet boundary. Prefix/payload bytes are copied once and
are never borrowed beyond the public call. There is no changed wire format,
coalescing delay, queue capacity, congestion policy or unreliable-delivery policy.

The storage deliberately is not pooled or returned on queue Pop: a packet packer
can retain a frame after removal. Go reachability retains a group until its last
referenced frame is released. This trades individual packet reclamation for
bounded group reclamation. It lowers object count, not necessarily peak RSS or
total allocation bytes at every size.

### Paired local microbenchmarks

Same Apple M4, Go 1.26.0, GOMAXPROCS=1, public API, identical test fixture, three
150 ms benchmark samples before and after the change. A preinitialized queue is
drained every iteration. Medians below are nanoseconds per whole batch, not
nanoseconds per packet and not encrypted WAN throughput.

| Payload / batch | Before ns | After ns | Time reduction | Allocations before/after |
| --- | ---: | ---: | ---: | ---: |
| 64 bytes x 8 | 337.6 | 227.1 | 32.7% | 16 / 2 |
| 64 bytes x 32 | 1307 | 806.9 | 38.3% | 64 / 2 |
| 1200 bytes x 8 | 1050 | 738.4 | 29.7% | 16 / 2 |
| 1200 bytes x 32 | 4253 | 2997 | 29.5% | 64 / 2 |

For 1200-byte single-packet calls the median was 139.4 -> 139.7 ns, still two
allocations. A 64-byte singleton changed 49.82 -> 51.85 ns; the patch does not
promise a singleton speedup. At 1200 x 32 allocation bytes slightly increased
41984 -> 42112 B/op due to allocation size classes, despite the object reduction.
At 1200 x 8 they decreased 10496 -> 9984 B/op.

Regressions cover caller mutation after return, frame lifetime after Pop, capacity
isolation on append, 128 inputs spanning queue-sized groups, validation with zero
accepted packets, and interrupted partial enqueue with an exact accepted count.
The existing ownership, prefix and receive-dispatch tests remain active.

## Change 2: event-driven delivery wait and a reproduced false-success case

`stream_acknowledged.go` previously allocated a 1 ms ticker per invocation and
locked the send stream on every tick until FIN acknowledgment or failure. This
is avoidable work when many connections await final delivery, particularly on
higher-RTT links. It is not necessarily a steady bulk-throughput bottleneck.

The candidate uses a lazily allocated notification channel per waiting stream,
shared by concurrent waiters. The real final-ACK, reset and shutdown transitions
notify waiters under the same mutex. Already-complete calls allocate no timer
or channel. There is no new worker, no per-packet channel send, and no polling.
Cancellation still returns an error; closing the application stream is not
proof that FIN arrived at the peer.

During this review a correctness issue was reproduced against the old code:
`closeForShutdown` sets `completed=true`, including a locally closed sender whose
FIN has been sent but is still outstanding. The old wait used completed+finSent
and could return nil in that case. A constructed regression with one outstanding
FIN frame failed before the fix with:

```
unacknowledged shutdown must fail, got <nil>
```

This is evidence of a delivery-confirmation bug in that state, not a claim that
an earlier production transfer was observed losing data. The candidate records
actual FIN acknowledgment separately from generic stream completion and keeps
a distinct abort error for the delivery-wait API. It does not change upstream
Write/Context shutdown semantics or treat remote reset code zero as success.

Tests exercise the actual ACK handler, application Close before ACK, FIN sent
before ACK, local/remote reset, connection shutdown, context cancellation, 32
concurrent waiters, late waiters, and shutdown after a real ACK. The previously
failing shutdown test now passes. The already-complete path measured zero
allocations in a local benchmark; this is not a measured whole-process CPU saving.

## Other optimization candidates reviewed but not changed

### BBR bandwidth sampling

`internal/congestion/bandwidth_sampler.go` stores per-packet pointer records in a
map and constructs BandwidthSample results. `bbrv3_sender.go` maintains additional
per-packet metadata. These are plausible allocation/hash-lookup costs shared by
stream and DATAGRAM traffic. A future prototype could reduce storage overhead,
but must preserve reordered ACKs, lost/discarded packet cleanup, app-limited
samples and per-path migration state. This review did not profile or modify
that path and makes no quantitative claim about its CPU share.

### HTTP/3 write framing

`http3/stream.go: Stream.Write` writes a DATA header and payload separately.
Combining writes may reduce scheduling, but partial writes, cancellation,
deadlines and frame-boundary ownership must remain correct. Blind buffering
can add a payload copy or reintroduce interactive latency. Not changed here.

### Native socket/offload integration

Further syscall savings depend on real capabilities of the consumer's packet
adapter, destination routing and relay behavior. Preserve existing ready-only
batching and do not import the previously rejected active eight-packet batching
or one-packet actor-queue experiments. No OS sysctl, queue size or BBR tuning
was altered by this work.

## Verification completed

- New DATAGRAM and delivery-wait regressions: race detector, five repetitions.
- Existing SendStream / Stream tests, including reset/retransmission behavior.
- Full `go test -short ./...` with Go 1.27.1.
- Race short suites for QUIC, HTTP/3, ACK handling, congestion and TLS handshake.
- `go vet ./...`, formatting checks and `git diff --check`.
- Cross-build of the QUIC tree for Windows amd64 and Linux ARMv7.
- Tailscale H3 integration at b4f1aa3cd: wgtransport and subpackages, quicip,
  transportprofile tests; targeted auth/revocation/MSS/batch/close race tests.
- Tailcat at f4af70cbe with the public b4f1aa3cd integration: full application,
  CLI and web tests, followed by serialized full race tests.
- Baseline/candidate test builds of both consumers for Linux amd64 and macOS
  arm64. All candidate QUIC overrides are in external test-only modfiles.

The existing application worktrees remain unchanged. Test artifacts are in the
Mac `tailscale-all/audits/shared-quic-cpu-20260923` directory.

## Explicit verification limits

A real-host isolated Tailscale throughput/CPU comparison was attempted but its
launch was blocked by the execution tool's safety check. It was not retried
through a different runner, agent or equivalent command. No fresh WAN rate or
production CPU percentage is reported. No cross-version old/new network matrix,
long-duration stress, production rollout or new release is claimed.

Only the isolated QUIC source branch is changed. Public application pins, release
tags, GitHub Actions settings, node identities and production daemons are not
modified. The next release should wait for fresh real-application throughput and
CPU measurements, including mixed old/new endpoints.

## Primary technical references

- https://quic-go.net/docs/quic/optimizations/
- https://quic-go.net/docs/quic/datagrams/
- https://www.rfc-editor.org/rfc/rfc9000.html

The upstream documentation explains native GSO and UDP buffer constraints. It is
not evidence of the fork's measured performance; the code/tests above are the
source for the local findings.
