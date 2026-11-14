# Upstream 0.65 integration

Based on upstream `v0.65.0`, commit `aa265c3fc6489e3bfa8071b333be95e0e887a51d`.
See the [upstream release notes](https://github.com/quic-go/quic-go/releases/tag/v0.65.0).

The merge includes upstream control/retransmission priority, connection-ID and
path migration fixes, HTTP/3 cancellation fixes, and qlog lifecycle/schema changes.
The fork retains bounded FIFO batch dequeue, temporary 0-RTT packet-budget
retention, early HTTP/3 datagram caching, packet-size recovery, and send telemetry.
When reliable work is pending, the fork keeps its existing one-datagram admission
limit after packing control and retransmission frames. Pure datagram packets
still pack the ready FIFO batch. This preserves reliable-stream progress.

The BBRv3 controller and pacer implementations are unchanged from the preceding
fork candidate. No new throughput or CPU improvement is claimed. Previous WAN
measurements on the 0.64 base do not validate this new base.

## Consumer update

`http3.HTTPStreamer.HTTPStream` returns `(*http3.Stream, error)`. Callers must
handle the error and explicitly write their response status before takeover.
The Tailscale tunnel handlers already write their status and now handle this
return value.

Tailcat's existing published Tailscale core also calls this interface. Merely
replacing QUIC in that older core will not compile. Its two stream-takeover call
sites need the same API update. Keep Tailcat's own core API extensions; replacing
its core with the general Tailscale branch is not an equivalent compatibility
fix. Its root module must also explicitly pin the uTLS fork. Existing Tailcat
deployments and their immutable dependency pins remain unchanged.

As before, library releases contain Go source. GitHub Actions remains disabled
and `.github` remains absent.
