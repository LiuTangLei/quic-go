package quic

import "github.com/quic-go/quic-go/internal/protocol"

// writeReadyBatch preserves datagram boundaries and existing OOB behavior.
// Every dequeued packet is released exactly once even when a writer fails.
func (h *sendQueue) writeReadyBatch(entries []queueEntry) error {
	defer func() {
		for _, e := range entries {
			e.buf.Release()
		}
	}()
	var data [sendQueueCapacity][]byte
	for i := 0; i < len(entries); {
		e := entries[i]
		if e.gsoSize != 0 || e.ecn != protocol.ECNUnsupported {
			if err := h.conn.Write(e.buf.Data, e.gsoSize, e.ecn); err != nil {
				return err
			}
			i++
			continue
		}
		n := 0
		for i+n < len(entries) && entries[i+n].gsoSize == 0 && entries[i+n].ecn == protocol.ECNUnsupported {
			data[n] = entries[i+n].buf.Data
			n++
		}
		var err error
		if n == 1 {
			err = h.conn.Write(data[0], 0, protocol.ECNUnsupported)
		} else {
			err = h.batchWrite(data[:n])
		}
		if err != nil {
			return err
		}
		i += n
	}
	return nil
}
