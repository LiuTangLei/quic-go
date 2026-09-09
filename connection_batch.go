package quic

import (
	"github.com/quic-go/quic-go/internal/ackhandler"
	"github.com/quic-go/quic-go/internal/monotime"
)

// sendPacketsWithPortableBatch batches variable-sized protected datagrams.
// Unlike UDP GSO, a vector has no equal-segment-size requirement. No extra
// padding, timer or speculative congestion credit is used. Receive processing
// is reconsidered after at most eight packets (kernel GSO allows larger groups).
func (c *Conn) sendPacketsWithPortableBatch(now monotime.Time, queue *sendQueue) error {
	for {
		var buffers [portablePacketBatchSize]*packetBuffer
		n := 0
		ecn := c.sentPacketHandler.ECNMode(true)
		stop := false
		for n < len(buffers) {
			if n > 0 && c.sentPacketHandler.ECNMode(true) != ecn {
				break
			}
			buf := getPacketBuffer()
			_, err := c.appendOneShortHeaderPacket(buf, c.maxPacketSize(), ecn, now)
			if err != nil {
				buf.Release()
				if err == errNothingToPack {
					stop = true
					break
				}
				for _, p := range buffers[:n] {
					p.Release()
				}
				return err
			}
			buffers[n] = buf
			n++
			mode := c.sentPacketHandler.SendMode(now)
			if mode == ackhandler.SendPacingLimited {
				c.resetPacingDeadline()
			}
			if mode != ackhandler.SendAny {
				stop = true
				break
			}
		}
		if n == 0 {
			return nil
		}
		queue.SendBatch(buffers[:n], ecn)
		if stop || queue.WouldBlock() {
			return nil
		}
		c.receivedPacketMx.Lock()
		incoming := !c.receivedPackets.Empty()
		c.receivedPacketMx.Unlock()
		if incoming {
			c.pacingDeadline = deadlineSendImmediately
			return nil
		}
	}
}
