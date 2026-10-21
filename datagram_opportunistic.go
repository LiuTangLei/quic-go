// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package quic

import (
	"errors"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/wire"
)

// TrySendDatagramsWithPrefix admits at most two low-priority datagrams only
// when the normal DATAGRAM queue is empty. It never waits or bypasses QUIC's
// pacing or congestion controller. false, nil means nothing was admitted.
func (c *Conn) TrySendDatagramsWithPrefix(prefix []byte, payloads [][]byte) (bool, error) {
	params := c.appliedPeerParams.Load()
	if params == nil || params.MaxDatagramFrameSize <= 0 {
		return false, errors.New("datagram support disabled")
	}
	if len(payloads) == 0 || len(payloads) > 2 {
		return false, errors.New("opportunistic datagram batch must contain one or two frames")
	}
	f := wire.DatagramFrame{DataLenPresent: true}
	limit := min(f.MaxDataLen(params.MaxDatagramFrameSize, c.version), protocol.ByteCount(c.maxPayloadSizeEstimate.Load()))
	for _, p := range payloads {
		if protocol.ByteCount(len(prefix)) > limit || protocol.ByteCount(len(p)) > limit-protocol.ByteCount(len(prefix)) {
			return false, &DatagramTooLargeError{MaxDatagramPayloadSize: int64(limit)}
		}
	}
	q := c.datagramQueue
	q.sendMx.Lock()
	select {
	case <-q.closed:
		q.sendMx.Unlock()
		return false, q.closeErr
	default:
	}
	if !q.sendQueue.Empty() {
		q.sendMx.Unlock()
		return false, nil
	}
	for _, p := range payloads {
		data := make([]byte, len(prefix)+len(p))
		copy(data, prefix)
		copy(data[len(prefix):], p)
		q.sendQueue.PushBack(&wire.DatagramFrame{DataLenPresent: true, Data: data})
	}
	q.sendMx.Unlock()
	q.hasData()
	return true, nil
}
