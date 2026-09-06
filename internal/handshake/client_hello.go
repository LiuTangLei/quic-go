package handshake

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/quic-go/quic-go/quicvarint"
	utls "github.com/refraction-networking/utls"
)

// quicTLSConn isolates client handshake customization from QUIC packet
// protection, congestion control and the ordinary crypto/tls server path.
// All event methods are called by the existing single-owner connection loop.
type quicTLSConn interface {
	Start(context.Context) error
	NextEvent() tls.QUICEvent
	Close() error
	HandleData(tls.QUICEncryptionLevel, []byte) error
	SetTransportParameters([]byte)
	ConnectionState() tls.ConnectionState
	StoreSession(*tls.SessionState) error
	SendSessionTicket(tls.QUICSessionTicketOptions) error
	ExportKeyingMaterial(string, []byte, int) ([]byte, error)
}

type standardQUICConn struct{ *tls.QUICConn }

func (c standardQUICConn) ExportKeyingMaterial(label string, context []byte, length int) ([]byte, error) {
	s := c.ConnectionState()
	return s.ExportKeyingMaterial(label, context, length)
}

// chromium-h3 is deliberately a Chromium-inspired TLS1.3 ClientHello, not an
// exact versioned browser/QUIC fingerprint. It keeps the real connection's
// transport parameters and nonzero CID. No fabricated origin, Google private
// parameters, ECH, ALPS, WebTransport capability or zero-CID claim is made.
const ChromiumH3Profile = "chromium-h3"

type browserQUICConn struct {
	c       *utls.UQUICConn
	config  *tls.Config
	params  []byte
	started bool
}

func newBrowserQUICConn(config *tls.Config, profile string) (quicTLSConn, error) {
	if profile != ChromiumH3Profile {
		return nil, fmt.Errorf("unsupported QUIC ClientHello profile %q", profile)
	}
	if len(config.NextProtos) != 1 || config.NextProtos[0] != "h3" {
		return nil, errors.New("chromium-h3 ClientHello requires h3 as its only ALPN")
	}
	// Do not silently discard authentication/resumption/ECH configuration that
	// this bounded adapter has not implemented. The caller can use the standard
	// TLS path, but an explicitly selected profile never silently downgrades.
	if config.ClientSessionCache != nil || len(config.EncryptedClientHelloConfigList) != 0 || config.EncryptedClientHelloRejectionVerify != nil || config.GetClientCertificate != nil || len(config.CurvePreferences) != 0 {
		return nil, errors.New("chromium-h3 does not support session cache, ECH, dynamic client certificates or custom curve restrictions")
	}
	if config.MinVersion > tls.VersionTLS13 || (config.MaxVersion != 0 && config.MaxVersion < tls.VersionTLS13) {
		return nil, errors.New("chromium-h3 requires TLS 1.3")
	}
	return &browserQUICConn{config: config.Clone()}, nil
}

func (c *browserQUICConn) SetTransportParameters(p []byte) { c.params = bytes.Clone(p) }

func (c *browserQUICConn) Start(ctx context.Context) error {
	if c.started {
		return errors.New("chromium-h3 TLS already started")
	}
	c.started = true
	p, err := browserHelloSpec(c.params, c.config.ServerName)
	if err != nil {
		return err
	}
	u := &utls.Config{
		Rand: c.config.Rand, Time: c.config.Time, RootCAs: c.config.RootCAs,
		ServerName: c.config.ServerName, InsecureSkipVerify: c.config.InsecureSkipVerify,
		MinVersion: utls.VersionTLS13, MaxVersion: utls.VersionTLS13, NextProtos: []string{"h3"},
		SessionTicketsDisabled: true, KeyLogWriter: c.config.KeyLogWriter,
		VerifyPeerCertificate: c.config.VerifyPeerCertificate,
	}
	if verify := c.config.VerifyConnection; verify != nil {
		u.VerifyConnection = func(s utls.ConnectionState) error { return verify(standardConnectionState(s)) }
	}
	for _, cert := range c.config.Certificates {
		uc := utls.Certificate{Certificate: cert.Certificate, PrivateKey: cert.PrivateKey, OCSPStaple: cert.OCSPStaple, SignedCertificateTimestamps: cert.SignedCertificateTimestamps, Leaf: cert.Leaf}
		for _, scheme := range cert.SupportedSignatureAlgorithms {
			uc.SupportedSignatureAlgorithms = append(uc.SupportedSignatureAlgorithms, utls.SignatureScheme(scheme))
		}
		u.Certificates = append(u.Certificates, uc)
	}
	c.c = utls.UQUICClient(&utls.QUICConfig{TLSConfig: u}, utls.HelloCustom)
	if err := c.c.ApplyPreset(p); err != nil {
		return err
	}
	c.c.SetTransportParameters(c.params)
	return translateTLSError(c.c.Start(ctx))
}

func (c *browserQUICConn) Close() error {
	if c.c == nil {
		return nil
	}
	return translateTLSError(c.c.Close())
}
func (c *browserQUICConn) HandleData(level tls.QUICEncryptionLevel, b []byte) error {
	if c.c == nil {
		return errors.New("chromium-h3 TLS not started")
	}
	return translateTLSError(c.c.HandleData(utls.QUICEncryptionLevel(level), b))
}
func (c *browserQUICConn) NextEvent() tls.QUICEvent {
	if c.c == nil {
		return tls.QUICEvent{Kind: tls.QUICNoEvent}
	}
	e := c.c.NextEvent()
	out := tls.QUICEvent{Level: tls.QUICEncryptionLevel(e.Level), Suite: e.Suite, Data: e.Data}
	switch e.Kind {
	case utls.QUICNoEvent:
		out.Kind = tls.QUICNoEvent
	case utls.QUICSetReadSecret:
		out.Kind = tls.QUICSetReadSecret
	case utls.QUICSetWriteSecret:
		out.Kind = tls.QUICSetWriteSecret
	case utls.QUICWriteData:
		out.Kind = tls.QUICWriteData
	case utls.QUICTransportParameters:
		out.Kind = tls.QUICTransportParameters
	case utls.QUICTransportParametersRequired:
		// Initial parameters were supplied before Start. Never advertise a made-up
		// placeholder when the real connection's parameters are unavailable.
		out.Kind = tls.QUICErrorEvent
		out.Err = errors.New("chromium-h3 missing QUIC transport parameters")
	case utls.QUICRejectedEarlyData:
		out.Kind = tls.QUICRejectedEarlyData
	case utls.QUICHandshakeDone:
		out.Kind = tls.QUICHandshakeDone
	default:
		out.Kind = tls.QUICErrorEvent
		out.Err = fmt.Errorf("unsupported chromium-h3 TLS event %d", e.Kind)
	}
	return out
}
func (c *browserQUICConn) ConnectionState() tls.ConnectionState {
	if c.c == nil {
		return tls.ConnectionState{}
	}
	return standardConnectionState(c.c.ConnectionState())
}
func (c *browserQUICConn) ExportKeyingMaterial(label string, context []byte, length int) ([]byte, error) {
	if c.c == nil {
		return nil, errors.New("chromium-h3 TLS not started")
	}
	s := c.c.ConnectionState()
	return s.ExportKeyingMaterial(label, context, length)
}
func (*browserQUICConn) StoreSession(*tls.SessionState) error {
	return errors.New("chromium-h3 session resumption is disabled")
}
func (*browserQUICConn) SendSessionTicket(tls.QUICSessionTicketOptions) error {
	return errors.New("chromium-h3 is client-only")
}

// The exporter is intentionally NOT transplanted into crypto/tls's unexported
// state using unsafe. Conn.ExportKeyingMaterial preserves it through the public
// connection API. Public certificate fields and callbacks keep their types.
func standardConnectionState(s utls.ConnectionState) tls.ConnectionState {
	return tls.ConnectionState{Version: s.Version, HandshakeComplete: s.HandshakeComplete, DidResume: s.DidResume, CipherSuite: s.CipherSuite, NegotiatedProtocol: s.NegotiatedProtocol, NegotiatedProtocolIsMutual: s.NegotiatedProtocolIsMutual, ServerName: s.ServerName, PeerCertificates: s.PeerCertificates, VerifiedChains: s.VerifiedChains, SignedCertificateTimestamps: s.SignedCertificateTimestamps, OCSPResponse: s.OCSPResponse, TLSUnique: s.TLSUnique}
}
func translateTLSError(err error) error {
	if err == nil {
		return nil
	}
	var alert utls.AlertError
	if errors.As(err, &alert) {
		return fmt.Errorf("uTLS QUIC: %w", tls.AlertError(alert))
	}
	return err
}

func browserHelloSpec(rawParams []byte, serverName string) (*utls.ClientHelloSpec, error) {
	params, err := realTransportParameters(rawParams)
	if err != nil {
		return nil, err
	}
	// A fresh spec and every mutable extension belongs to exactly one dial.
	extensions := []utls.TLSExtension{
		&utls.QUICTransportParametersExtension{TransportParameters: params},
		&utls.ALPNExtension{AlpnProtocols: []string{"h3"}},
		&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13}},
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384}},
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519MLKEM768}, {Group: utls.X25519}}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{utls.ECDSAWithP256AndSHA256, utls.PSSWithSHA256, utls.PKCS1WithSHA256, utls.ECDSAWithP384AndSHA384, utls.PSSWithSHA384, utls.PKCS1WithSHA384, utls.PSSWithSHA512, utls.PKCS1WithSHA512}},
		&utls.StatusRequestExtension{}, &utls.SCTExtension{},
		&utls.UtlsCompressCertExtension{Algorithms: []utls.CertCompressionAlgo{utls.CertCompressionBrotli}},
	}
	if serverName != "" {
		extensions = append(extensions, &utls.SNIExtension{})
	}
	if err := secureShuffle(extensions); err != nil {
		return nil, err
	}
	return &utls.ClientHelloSpec{TLSVersMin: utls.VersionTLS13, TLSVersMax: utls.VersionTLS13, CipherSuites: []uint16{utls.TLS_AES_128_GCM_SHA256, utls.TLS_AES_256_GCM_SHA384, utls.TLS_CHACHA20_POLY1305_SHA256}, CompressionMethods: []uint8{0}, Extensions: extensions}, nil
}

// Reorder only what this connection really supports. Do not substitute browser
// flow-control limits for the actual limits, or add unsupported capabilities.
func realTransportParameters(raw []byte) (utls.TransportParameters, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing real QUIC transport parameters")
	}
	r := bytes.NewReader(raw)
	seen := map[uint64]bool{}
	out := utls.TransportParameters{}
	for r.Len() > 0 {
		id, err := quicvarint.Read(r)
		if err != nil {
			return nil, err
		}
		n, err := quicvarint.Read(r)
		if err != nil || n > uint64(r.Len()) {
			return nil, errors.New("invalid transport parameter length")
		}
		if seen[id] {
			return nil, errors.New("duplicate transport parameter")
		}
		seen[id] = true
		b := make([]byte, int(n))
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		out = append(out, &utls.FakeQUICTransportParameter{Id: id, Val: b})
	}
	// One bounded RFC 9000 reserved parameter exercises unknown-parameter
	// handling without claiming ECH or a private Google parameter's semantics.
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	grease := 31*uint64(binary.BigEndian.Uint32(random[:4])) + 27
	for seen[grease] {
		grease += 31
	}
	out = append(out, &utls.FakeQUICTransportParameter{Id: grease, Val: bytes.Clone(random[4:])})
	if err := secureShuffle(out); err != nil {
		return nil, err
	}
	return out, nil
}
func secureShuffle[T any](v []T) error {
	for i := len(v) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		v[i], v[j.Int64()] = v[j.Int64()], v[i]
	}
	return nil
}
