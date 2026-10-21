// Derived from quic-go v0.62.0. See LICENSE.
// Bounded receive ring and local drop accounting; wire semantics are unchanged.
package quic

import (
	"context"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/utils/ringbuffer"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"sync"
)

const (
	maxDatagramSendQueueLen            = 32
	maxDatagramRcvQueueLen             = 1024
	maxDatagramRcvQueueBytes           = 2 << 20
	TunnelDatagramReceiveQueueCapacity = maxDatagramRcvQueueLen
)

type datagramQueue struct {
	sendMx                     sync.Mutex
	sendQueue                  ringbuffer.RingBuffer[*wire.DatagramFrame]
	sent                       chan struct{}
	rcvMx                      sync.Mutex
	rcvQueue                   [][]byte
	rcvHead, rcvSize, rcvBytes int
	rcvDrops                   uint64
	rcvd                       chan struct{}
	closeErr                   error
	closed                     chan struct{}
	hasData                    func()
	qlogger                    qlogwriter.Recorder
	logger                     utils.Logger
}

func newDatagramQueue(hasData func(), logger utils.Logger, qlogger qlogwriter.Recorder) *datagramQueue {
	return &datagramQueue{hasData: hasData, rcvd: make(chan struct{}, 1), sent: make(chan struct{}, 1), closed: make(chan struct{}), logger: logger, qlogger: qlogger}
}
func (h *datagramQueue) Add(f *wire.DatagramFrame) error {
	h.sendMx.Lock()
	for {
		// don't queue frames after the connection is closed
		select {
		case <-h.closed:
			h.sendMx.Unlock()
			return h.closeErr
		default:
		}

		if h.sendQueue.Len() < maxDatagramSendQueueLen {
			h.sendQueue.PushBack(f)
			h.sendMx.Unlock()
			h.hasData()
			return nil
		}
		select {
		case <-h.sent:
		default:
		}
		h.sendMx.Unlock()
		// The queue is full. Wait until a frame is dequeued.
		select {
		case <-h.closed:
			return h.closeErr
		case <-h.sent:
		}
		h.sendMx.Lock()
	}
}
func (h *datagramQueue) Peek() *wire.DatagramFrame {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	if h.sendQueue.Empty() {
		return nil
	}
	return h.sendQueue.PeekFront()
}
func (h *datagramQueue) Pop() {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	_ = h.sendQueue.PopFront()
	select {
	case h.sent <- struct{}{}:
	default:
	}
}
func (h *datagramQueue) HandleDatagramFrame(f *wire.DatagramFrame) {
	h.rcvMx.Lock()
	if h.rcvSize >= maxDatagramRcvQueueLen || len(f.Data) > maxDatagramRcvQueueBytes-h.rcvBytes {
		h.rcvDrops++
		h.rcvMx.Unlock()
		if h.logger.Debug() {
			h.logger.Debugf("Discarding received DATAGRAM frame (%d bytes payload)", len(f.Data))
		}
		if h.qlogger != nil {
			h.qlogger.RecordEvent(qlog.DatagramDropped{Direction: qlog.DirectionReceiving, Raw: qlog.RawInfo{Length: len(f.Data)}, Trigger: "buffer_full"})
		}
		return
	}
	data := make([]byte, len(f.Data))
	copy(data, f.Data)
	if h.rcvQueue == nil {
		h.rcvQueue = make([][]byte, maxDatagramRcvQueueLen)
	}
	h.rcvQueue[(h.rcvHead+h.rcvSize)%maxDatagramRcvQueueLen] = data
	h.rcvSize++
	h.rcvBytes += len(data)
	select {
	case h.rcvd <- struct{}{}:
	default:
	}
	h.rcvMx.Unlock()
}
func (h *datagramQueue) Receive(ctx context.Context) ([]byte, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	for {
		h.rcvMx.Lock()
		if h.rcvSize > 0 {
			data := h.rcvQueue[h.rcvHead]
			h.rcvQueue[h.rcvHead] = nil
			h.rcvHead = (h.rcvHead + 1) % maxDatagramRcvQueueLen
			h.rcvSize--
			h.rcvBytes -= len(data)
			h.rcvMx.Unlock()
			return data, nil
		}
		h.rcvMx.Unlock()
		select {
		case <-h.rcvd:
			continue
		case <-h.closed:
			return nil, h.closeErr
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}
func (h *datagramQueue) CloseWithError(e error) { h.closeErr = e; close(h.closed) }

// DatagramReceiveQueueStats distinguishes local unreliable-delivery loss from
// transport loss. Returned values never contain keys or payloads.
func (c *Conn) DatagramReceiveQueueStats() (queued, queuedBytes int, dropped uint64) {
	q := c.datagramQueue
	if q == nil {
		return 0, 0, 0
	}
	q.rcvMx.Lock()
	defer q.rcvMx.Unlock()
	return q.rcvSize, q.rcvBytes, q.rcvDrops
}
