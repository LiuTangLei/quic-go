# Shared QUIC / HTTP3 transport fork

Base: upstream quic-go v0.63.0, commit 9d085cc690f7c96451e8ae5659eb0e64671da47a.
This is a protocol library, not a combined Tailscale/Tailcat application.
The canonical Go module path is retained so applications can use a versioned
`replace github.com/quic-go/quic-go => github.com/LiuTangLei/quic-go <tag>`.
Do not point a release at an unpublished tag or a local filesystem replacement.

## Upstream 0.63 integration (2026-09-22)

The upgrade merges the official v0.63.0 tag into v0.62.0-tailscale.4, retaining
its bounded DATAGRAM send batches, authenticated receive dispatch, browser
ClientHello profile, congestion controllers and public TLS exporter. The
compatibility FIN-acknowledgment API remains available to existing stream
consumers; it does not introduce application commands or a second product.

Do not substitute v0.62.0-tailscale.5 as an equivalent performance baseline:
that split branch was based on an earlier tree and lacks the .4 DATAGRAM batch
and direct-receive additions. No old tag is rewritten by this upgrade.

HTTP/3 0.63 server request URLs no longer carry Scheme/Host for ordinary and
Extended CONNECT requests; authority remains in Request.Host. Extended
CONNECT RequestURI is now the :path value. Stream/application errors are
wrapped as *http3.Error and support unwrapping. Consumers must not depend on
the previous URL representation or require a direct QUIC error type assertion.

Both consuming projects were tested with this source: the Tailscale H3/IP
engine (including node authentication, MSS, batch and tsnet tests) and
LiuTangLei/tailcat-quic. This is compatibility evidence, not a new WAN speed or
browser-indistinguishability claim. No installed service is changed by a
library source update.

## Repository boundaries

- LiuTangLei/tailscale is the VPN application and its integration code.
- LiuTangLei/tailcat-quic is the separate Tailcat application.
- LiuTangLei/quic-go is this reusable protocol implementation.
- LiuTangLei/wireguard-go supplies native WG/AWG and shared TUN primitives;
  using its TUN code does not mean H3 payloads are encrypted with WireGuard.

Splitting applications does not require duplicating an entire protocol
library. Historical tailcat-tailscale / tailcat-quic-go mirror repositories
are not the primary application locations and are not deleted or rewritten
here. Existing consumers keep exact version pins; do not redirect dependencies
through a similarly named mirror without a separately reviewed migration.

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
