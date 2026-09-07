# Optional H3 client handshake profile

`Config.ClientHelloProfile = "chromium-h3"` selects a per-connection,
Chromium-inspired TLS 1.3 ClientHello, implemented through the public uTLS
`UQUICClient` API. Empty selects the unchanged standard `crypto/tls` path.
Servers always use standard `crypto/tls`. Existing BBR, CID demultiplexing,
QUIC packet protection, congestion control, queues and HTTP/3 remain in place.

This is **not** a byte-identical Chrome 146/150 or Safari QUIC fingerprint.
In particular the ordinary nonzero connection IDs, Initial packet-number and
packetizer behavior are retained for shared-socket mesh compatibility. The
flow-control, DATAGRAM and stream-limit transport parameters are the actual
connection parameters, not values copied from a browser sample. Parameters and
TLS extensions are shuffled per fresh dial; one correctly encoded, bounded RFC
9000 reserved transport parameter is added. No invented Google RTT extension,
WebTransport support, third-party domain, ECH or ALPS capability is advertised.
The ClientHello uses Chromium-style TLS 1.3 cipher order and key-share groups,
implemented Brotli certificate compression and standard TLS status extensions.
Its Chromium-style signature list supports ECDSA and RSA server certificates;
Ed25519 server certificates require the standard TLS path.

## Authentication

The standard TLS config's roots, name verification, static certificates,
VerifyPeerCertificate and VerifyConnection remain enforced. Certificate/key
material and mutable templates are never shared or derived from another peer.
Unsupported ECH, custom curve restrictions, dynamic client certificates and
resumption caches fail explicitly rather than being silently ignored. This
first client adapter does fresh TLS 1.3 handshakes (connection reuse still
works); opportunistic `DialEarly`, including the default HTTP/3 transport,
waits for the full handshake and never sends 0-RTT. It does not promise session
resumption/0-RTT fingerprinting.

Call `Conn.ExportKeyingMaterial(label, context, length)` for channel-bound
application authentication. It obtains the exporter from the actual handshake
implementation. Do not call `ConnectionState().TLS.ExportKeyingMaterial` on a
profiled connection: Go's public TLS state cannot carry uTLS's private exporter.
There is no unsafe memory conversion. Public TLS state and verification
callbacks retain their normal standard-library certificate types. The uTLS
version does not publicly expose the negotiated curve or HelloRetryRequest
status, so the Go 1.26 `CurveID` and `HelloRetryRequest` fields remain zero on
the profiled path and must not be used to infer the negotiated key exchange.

`ConnectionState().ClientHelloProfile` reports the actual local client path;
it is empty on standard handshakes and incoming server connections. The
consumer, not this library, decides which authenticated peer is an eligible
server. Missing or revoked authorization is never inferred from a fingerprint.

## Evidence

Tests exercise actual serialized TLS ClientHello data, empty QUIC session IDs,
real transport-parameter roundtrips, per-dial extension order, pinned/public-root
verification rejection with preserved error causes, exporter equality and
context separation, default HTTP/3 dialing and connection reuse, full QUIC
handshakes, bidirectional DATAGRAMs, and eight concurrent standard/profiled
connections sharing one UDP socket and nonzero CIDs. Full upstream short-mode
and focused race suites are retained. This is protocol/implementation evidence,
not GFW testing or a claim of browser indistinguishability.

Dependency: refraction-networking/utls v1.8.2, commit
8fe0b08e9a0e7e2d08b268f451f2c79962e6acd0 (BSD-3-Clause). Browser traits are
informed by the project's QUIC interface and uQUIC's source-reference profile;
the older uQUIC transport stack and flawed fixed-width RTT helper were not
copied. The adapter and tests are maintained in this fork.
