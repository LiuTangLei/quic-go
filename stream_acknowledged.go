// Copyright (c) 2026 LiuTangLei contributors.
// SPDX-License-Identifier: BSD-3-Clause

package quic

import (
	"context"
	"time"
)

// WaitWriteAcknowledged waits until the peer acknowledges all stream bytes and
// the final FIN. Unlike Context, calling Close alone does not satisfy it.
// The caller must close the sending side; cancellation/reset is an error.
// Intended for one-shot applications about to terminate their QUIC connection.
func (s *Stream) WaitWriteAcknowledged(ctx context.Context) error {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.sendStr.mutex.Lock()
		complete := s.sendStr.completed && s.sendStr.finSent && s.sendStr.resetErr == nil
		var err error = s.sendStr.shutdownErr
		if s.sendStr.resetErr != nil {
			err = s.sendStr.resetErr
		}
		s.sendStr.mutex.Unlock()
		if complete {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
