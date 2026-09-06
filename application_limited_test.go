package quic

import (
	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"testing"
)

type limitedHandler struct {
	ackhandler.SentPacketHandler
	calls int
}

func (h *limitedHandler) OnApplicationLimited() { h.calls++ }

func TestBBRApplicationLimitedOnlyWhenQueuesEmpty(t *testing.T) {
	h := new(limitedHandler)
	c := &Conn{config: &Config{EnableBBR: true}, handshakeConfirmed: true, sentPacketHandler: h}
	c.datagramQueue = newDatagramQueue(func() {}, utils.DefaultLogger)
	if err := c.datagramQueue.Add(&wire.DatagramFrame{Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	c.maybeNotifyApplicationLimited()
	if h.calls != 0 {
		t.Fatal("pending data mislabeled application-limited")
	}
	c.datagramQueue.Pop()
	c.maybeNotifyApplicationLimited()
	if h.calls != 1 {
		t.Fatal("empty sender did not notify sampler")
	}
	c.handshakeConfirmed = false
	c.maybeNotifyApplicationLimited()
	if h.calls != 1 {
		t.Fatal("unconfirmed handshake notified")
	}
	c.handshakeConfirmed = true
	c.config.EnableBBR = false
	c.maybeNotifyApplicationLimited()
	if h.calls != 1 {
		t.Fatal("default Reno path changed")
	}
}
