# Tailscale native-IP datagram fork (experimental)

Base: upstream quic-go v0.62.0, commit 793f74d8e03368c5aded128af6f48d21dbb47f73.
The canonical Go module path is retained so applications can use a versioned
`replace github.com/quic-go/quic-go => github.com/LiuTangLei/quic-go <tag>`.
Do not point a release at an unpublished tag or a local filesystem replacement.

## Changes

* Optional per-connection `chromium-h3` uTLS ClientHello and a public, safe
  `Conn.ExportKeyingMaterial` bridge. Ordinary clients and all TLS servers keep
  crypto/tls. See CLIENTHELLO.md for exact scope and non-browser-matching limits.

* Bounded receive rings: 1024 transport DATAGRAMs / 2 MiB, 256 HTTP Datagrams.
  Local receive drops are reported separately from network loss. The send queue
  remains bounded and datagrams remain unreliable.
* HyStart ignores application-limited RTT samples instead of exhausting slow
  start while a tunnel direction only carries small inner TCP ACKs.
* CUBIC and BBRv1 are opt-in per-connection policies. Zero Config retains the
  upstream Reno default. EnableBBRCongestionControl / EnableCubicCongestionControl
  must be called before Dial/Listen. Selecting both flags is rejected.
* Controller, cwnd, in-flight and slow-start diagnostics are independent atomic
  samples. They carry no payload or key material.
* BBR is instantiated with the actual connection RTT statistics and initial
  datagram size. PMTU growth and path migration preserve the selected policy
  while resetting per-path measurements. The generic pacer's Reno/CUBIC 25%
  headroom is not applied on top of BBR's existing gain.
* Sampling uses unique congestion packet numbers across QUIC's distinct Initial,
  Handshake and application spaces. Discarded keyspaces and PMTU probes release
  sampler records. Recovery boundaries advance on loss, not every ACK.

TLS, certificate verification, QUIC packet protection, ACK/loss detection,
anti-amplification and wire protocols are unchanged. There is no congestion
bypass, unlimited queue, fixed-rate sender or automatic connection cycling.

## Provenance and licenses

The BBRv1 port is adapted from tdragoun/quic-go branch bbr_v1, commits
9cc335e6f4df63b949b4998ddd063ed52f49847f and
a07eb48492755adb24d4f278a92f5e054f1eccad. Both original commits and author
attribution are retained in history. Their comments credit For-ACGN/quic-bbr
and Google's QUICHE implementation. See the original MIT LICENSE and the
included LICENSE.chromium for the QUICHE-derived algorithm. This fork adds
integration and regression fixes; no endorsement by those projects is implied.

## Verification

Run `go test ./... -short`, and race tests for internal/congestion,
internal/ackhandler and http3. Extra regressions exercise underfilled HyStart,
first-packet sampling, recovery progress with continuing sends, exact pacing
rate, MTU initialization, discard cleanup and cross-space sampler identity.
WAN results live in the consuming Tailscale artifact directory. A successful
unit test is not evidence of a universal WAN throughput guarantee.
