# QUIC 0.64.1 integration for review

This branch merges upstream `v0.64.1` (`ed90516797ec8ed7314c2b163797eca58fd5a2b8`).
It retains the canonical `github.com/quic-go/quic-go` module path and the existing
BBRv3, profiled ClientHello, authenticated-ticket and bounded DATAGRAM changes.
It is review input, not a new application or library release.

uTLS is based on its latest stable `v1.8.2`, plus our session-event/early-data
commit `7effde6a93dc68c3535eaa590b6d28c503e18ff6`. Its resolvable fork version is
`github.com/LiuTangLei/utls v1.8.3-0.20261021023852-7effde6a93dc`.
The pseudo-version identifies that exact patch; it is not an upstream 1.8.3
release. No local module path is needed.

Go does not inherit dependency `replace` directives. Application consumers must
pin both the QUIC fork and this uTLS fork in their own `go.mod`. Build and test
with `GOWORK=off` to validate those pins.

## Merge decisions

- Use upstream's connection-wide packet-number generator and shared history.
  BBR sampling, discard notifications and spurious-loss handling now use those
  numbers directly; the old extra sampling sequence is removed. Preserve live
  flight statistics, discard cleanup and application-limited notifications.
- BBRv3 remains the production default and the controller after migration.
  Deprecated selector arguments do not select a different controller. ECN
  validation remains disabled because this BBRv3 policy has no CE response.
- Adapt BBR to upstream's explicit-rate pacer API. BBR passes its own gained
  pacing rate without adding Reno/CUBIC's gain again.
- A single late ACK is not proof that an entire BBR recovery episode was
  spurious. The new per-packet notification does not undo BBR model bounds;
  explicit whole-episode recovery remains a separate method.
- Upstream's Initial-packet `EMSGSIZE` fallback can reduce the packet size to
  1200 on the same path. BBR now accepts it and reduces its byte window with the
  packet size, retaining its controller and congestion-window bound. The new
  regression reproduced the old panic before this fix.
- Retain the bounded receive ring, borrowed synchronous DATAGRAM parsing, FIFO
  ready-only packing and bounded dequeue. Adopt upstream's immediate canceled
  receive/closed-send errors and dropped-DATAGRAM qlog events. Recorder callbacks
  run outside the send-queue mutex.
- Two upstream sent-packet tests assert Reno/CUBIC-specific window undo and qlog
  states. They now explicitly inject that controller into their test fixture;
  their assertions are retained. Separate tests protect the BBRv3 production
  default, migration and conservative single-spurious-packet behavior.
- `.github` stays absent, as required by this shared library's policy. No FEC,
  new congestion-controller menu, application UDP repair or queue expansion.

## Validation scope

```sh
GOWORK=off GOTOOLCHAIN=go1.27.3 go test -mod=readonly -race -short ./... -count=1
GOWORK=off GOTOOLCHAIN=go1.27.3 go mod verify
```

The audit directory `audits/h3-deps-1.104.2-20261021` in the parent workspace
records commands, logs, pins and consumer validation. Short tests do not cover
every long-running stress test or WAN path. Earlier mixed-load UDP gate failures
remain release blockers; dependency resolution does not qualify those experiments.
