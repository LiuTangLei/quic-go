package handshake

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go/quicvarint"
)

// Inspect the bytes actually produced by the TLS engine for QUIC CRYPTO, not
// just the configured template. UDP Initial layout remains quic-go's normal
// layout and is deliberately outside this ClientHello-only assertion.
func TestChromiumH3EmittedClientHello(t *testing.T) {
	params := quicvarint.Append(nil, 0x0f)
	params = quicvarint.Append(params, 8)
	params = append(params, 1, 2, 3, 4, 5, 6, 7, 8)
	params = quicvarint.Append(params, 0x04)
	params = quicvarint.Append(params, 2)
	params = append(params, 0x44, 0x00)
	params = quicvarint.Append(params, 0x20)
	params = quicvarint.Append(params, 4)
	params = append(params, 0x80, 1, 0, 0)
	for _, name := range []string{"", "owned-h3.test"} {
		t.Run(name, func(t *testing.T) {
			orders := map[string]bool{}
			for range 12 {
				c, err := newBrowserQUICConn(&tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: name, InsecureSkipVerify: true}, ChromiumH3Profile)
				if err != nil {
					t.Fatal(err)
				}
				c.SetTransportParameters(params)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				if err := c.Start(ctx); err != nil {
					cancel()
					t.Fatal(err)
				}
				var hello []byte
				for {
					e := c.NextEvent()
					if e.Kind == tls.QUICNoEvent {
						break
					}
					if e.Kind == tls.QUICErrorEvent {
						t.Fatal(e.Err)
					}
					if e.Kind == tls.QUICWriteData {
						if e.Level != tls.QUICEncryptionLevelInitial {
							t.Fatal("ClientHello at wrong encryption level")
						}
						hello = append(hello, e.Data...)
					}
				}
				c.Close()
				cancel()
				if len(hello) < 40 || hello[0] != 1 || int(hello[1])<<16|int(hello[2])<<8|int(hello[3]) != len(hello)-4 {
					t.Fatal("malformed ClientHello")
				}
				r := bytes.NewReader(hello[4:])
				v := read16(t, r)
				if v != 0x0303 {
					t.Fatalf("legacy_version=%x", v)
				}
				random := make([]byte, 32)
				io.ReadFull(r, random)
				sessionLen, err := r.ReadByte()
				if err != nil || sessionLen != 0 {
					t.Fatal("QUIC nonempty session ID")
				}
				n := read16(t, r)
				if n != 6 {
					t.Fatalf("not three TLS1.3 suites: %d", n)
				}
				for _, want := range []uint16{0x1301, 0x1302, 0x1303} {
					if got := read16(t, r); got != want {
						t.Fatalf("cipher order %x want %x", got, want)
					}
				}
				compression, _ := r.ReadByte()
				zero, _ := r.ReadByte()
				if compression != 1 || zero != 0 {
					t.Fatal("compression malformed")
				}
				extLen := read16(t, r)
				if int(extLen) != r.Len() {
					t.Fatal("extension length mismatch")
				}
				ids := []uint16{}
				ext := map[uint16][]byte{}
				for r.Len() > 0 {
					id := read16(t, r)
					n := read16(t, r)
					if int(n) > r.Len() {
						t.Fatal("extension overflow")
					}
					b := make([]byte, n)
					io.ReadFull(r, b)
					if _, ok := ext[id]; ok {
						t.Fatal("duplicate extension")
					}
					ext[id] = b
					ids = append(ids, id)
				}
				orders[fmt.Sprint(ids)] = true
				if _, sni := ext[0]; sni != (name != "") {
					t.Fatal("SNI policy mismatch")
				}
				if !bytes.Equal(ext[16], []byte{0, 3, 2, 'h', '3'}) {
					t.Fatalf("ALPN=%x", ext[16])
				}
				if !bytes.Equal(ext[43], []byte{2, 3, 4}) {
					t.Fatalf("supported_versions=%x", ext[43])
				}
				qtp := decodeProfileParams(t, ext[0x39])
				original := decodeProfileParams(t, params)
				for id, want := range original {
					if !bytes.Equal(qtp[id], want) {
						t.Fatalf("real QUIC parameter %x changed", id)
					}
				}
				if len(qtp) != len(original)+1 {
					t.Fatal("unexpected advertised QUIC capability")
				}
				for id := range qtp {
					if _, ok := original[id]; !ok && id%31 != 27 {
						t.Fatal("non-reserved fake transport parameter")
					}
				}
				if _, ok := ext[27]; !ok {
					t.Fatal("missing implemented Brotli certificate compression")
				}
				if _, ok := ext[0xfe0d]; ok {
					t.Fatal("profile must not claim ECH")
				}
			}
			if len(orders) < 2 {
				t.Fatal("extension order unexpectedly fixed across independent dials")
			}
		})
	}
}
func read16(t *testing.T, r *bytes.Reader) uint16 {
	t.Helper()
	var x uint16
	if err := binary.Read(r, binary.BigEndian, &x); err != nil {
		t.Fatal(err)
	}
	return x
}
func decodeProfileParams(t *testing.T, b []byte) map[uint64][]byte {
	t.Helper()
	r := bytes.NewReader(b)
	m := map[uint64][]byte{}
	for r.Len() > 0 {
		id, e := quicvarint.Read(r)
		if e != nil {
			t.Fatal(e)
		}
		n, e := quicvarint.Read(r)
		if e != nil || n > uint64(r.Len()) {
			t.Fatal("parameter overflow")
		}
		v := make([]byte, n)
		io.ReadFull(r, v)
		if _, ok := m[id]; ok {
			t.Fatal("duplicate parameter")
		}
		m[id] = v
	}
	return m
}
