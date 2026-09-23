# Shared QUIC / HTTP3

A shared QUIC/HTTP3 fork for **Tailscale** and **Tailcat-QUIC**, with **lightly tuned BBRv3** and low-overhead packet processing.

This is a Go transport library, not a combined VPN/proxy application. Tailscale and Tailcat keep their own application repositories, configuration and release cycles.

## Current development policy

All connections use lightly tuned BBRv3, including a nil or zero `quic.Config` and a connection after path migration. No congestion-controller selection is needed. The diagnostic name stays `bbr-v3`.

```go
config := &quic.Config{
    EnableDatagrams: true,
}
// Dial / Listen normally; lightly tuned BBRv3 is automatic.
```

This policy is on the `perf/bbrv3-default-20260923` development branch. It does not retroactively change the immutable `v0.63.0-quic.2` release or installed applications. Existing application source interfaces remain compatible; details are in [BBRv3.md](BBRv3.md).

## Transport features

- QUIC v1, TLS 1.3 and real HTTP/3 framing on the upstream 0.63 base.
- Reliable streams for byte-stream services and bounded QUIC DATAGRAM paths for IP/UDP traffic.
- Ready-only packet batching and group-owned DATAGRAM storage without waiting to fill a batch.
- Inline delivery-sampling records to reduce per-packet heap allocation.
- Event-driven final-FIN acknowledgment instead of periodic polling.
- Optional per-connection Chromium-inspired ClientHello; see [CLIENTHELLO.md](CLIENTHELLO.md) for its limitations.

Authentication, encryption, retransmission rules, pacing, loss response, anti-amplification and queue bounds remain enforced. More frequent bandwidth probing is not a bypass of congestion control or a guarantee of better performance on every network.

## Consumers

- [Tailscale fork](https://github.com/LiuTangLei/tailscale): QUIC/H3 native IP integration.
- [Tailcat-QUIC](https://github.com/LiuTangLei/tailcat-quic): independent H3-only application.

Consumers retain the canonical module import `github.com/quic-go/quic-go` and pin a published immutable fork version through `go.mod`. Do not use a local replacement in a published application. Library releases contain source, not platform-specific client installers.

## Verification and development

```sh
go test -short ./...
go test -race -short . ./http3 ./internal/ackhandler ./internal/congestion ./internal/handshake
go vet ./...
```

See [the latest optimization record](BBRV3_TUNING_20260923.md), [the preceding review](PERFORMANCE_REVIEW_20260923.md) and [fork provenance](FORK.md). Microbenchmarks are not WAN throughput results, and every report identifies its actual test scope.

GitHub Actions is disabled for this library and `.github` intentionally remains absent. Builds and checks run locally or on explicitly selected test hosts.

## License

Upstream quic-go code retains its [MIT license](LICENSE). Adapted components retain their per-file notices and additional attribution in [FORK.md](FORK.md), [BBRv3.md](BBRv3.md) and `LICENSE.chromium`. This independent fork is not endorsed by the upstream maintainers.
