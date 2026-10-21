// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package quic

import (
	"bytes"
	"github.com/quic-go/quic-go/internal/wire"
	"testing"
)

func TestOpportunisticDatagramsYieldAndAtomicOwnership(t *testing.T) {
	c := prefixTestConn(1400)
	c.datagramQueue.Add(&wire.DatagramFrame{Data: []byte{9}})
	if ok, e := c.TrySendDatagramsWithPrefix(nil, [][]byte{{1}}); ok || e != nil {
		t.Fatal("did not yield to normal traffic", ok, e)
	}
	c.datagramQueue.Pop()
	a, b := []byte{1, 2}, []byte{3, 4}
	prefix := []byte{7}
	if ok, e := c.TrySendDatagramsWithPrefix(prefix, [][]byte{a, b}); !ok || e != nil {
		t.Fatal(ok, e)
	}
	clear(a)
	clear(b)
	clear(prefix)
	if !bytes.Equal(c.datagramQueue.Peek().Data, []byte{7, 1, 2}) {
		t.Fatal("borrowed buffer retained")
	}
	c.datagramQueue.Pop()
	if !bytes.Equal(c.datagramQueue.Peek().Data, []byte{7, 3, 4}) {
		t.Fatal("second buffer changed")
	}
	c.datagramQueue.Pop()
	if ok, e := c.TrySendDatagramsWithPrefix(nil, [][]byte{{1}, make([]byte, 1500)}); ok || e == nil || c.datagramQueue.Peek() != nil {
		t.Fatal("oversize batch partially admitted")
	}
	if ok, e := c.TrySendDatagramsWithPrefix(nil, make([][]byte, 3)); ok || e == nil {
		t.Fatal("unbounded opportunistic batch")
	}
}
