// Copyright (c) 2026 LiuTangLei contributors.
// SPDX-License-Identifier: BSD-3-Clause

package quic

import "context"

// WaitWriteAcknowledged waits until the peer acknowledges all stream bytes and
// the final FIN. Unlike Context, calling Close alone does not satisfy it.
// The caller must close the sending side; cancellation/reset is an error.
// Intended for one-shot applications about to terminate their QUIC connection.
func (s *Stream) WaitWriteAcknowledged(ctx context.Context) error {
	for {
		str := s.sendStr
		str.mutex.Lock()
		complete := str.finAcknowledged && str.resetErr == nil
		err := str.ackShutdownErr
		if err == nil {
			err = str.shutdownErr
		}
		if str.resetErr != nil {
			err = str.resetErr
		}
		if complete {
			str.mutex.Unlock()
			return nil
		}
		if err != nil {
			str.mutex.Unlock()
			return err
		}
		if err := ctx.Err(); err != nil {
			str.mutex.Unlock()
			return err
		}
		if str.ackWait == nil {
			str.ackWait = make(chan struct{})
		}
		wake := str.ackWait
		str.mutex.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
			// Recheck the state under the same lock. A reset or connection
			// abort is a wakeup, not successful delivery.
		}
	}
}

// Called with SendStream.mutex held at real FIN acknowledgment, reset, or
// connection shutdown. No per-ACK signaling, polling timer, or new worker.
func (s *SendStream) signalAcknowledgmentLocked() {
	if s.ackWait != nil {
		close(s.ackWait)
		s.ackWait = nil
	}
}
