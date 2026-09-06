package quic

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
)

func browserListener(t *testing.T, tc *tls.Config, qc *Config) (*Listener, error) {
	t.Helper()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	transport := &Transport{Conn: udp, ConnectionIDLength: 8}
	t.Cleanup(func() { transport.Close(); udp.Close() })
	return transport.Listen(tc, qc)
}
func browserDial(t *testing.T, ctx context.Context, address string, tc *tls.Config, qc *Config) (*Conn, error) {
	t.Helper()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	transport := &Transport{Conn: udp, ConnectionIDLength: 8}
	t.Cleanup(func() { transport.Close(); udp.Close() })
	remote, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return nil, err
	}
	return transport.Dial(ctx, remote, tc, qc)
}
