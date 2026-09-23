package quic

import (
	"context"
	"errors"
	"testing"
)

func TestWaitWriteAcknowledgedDoesNotMistakeShutdownForFINACK(t *testing.T) {
	str := newSendStream(context.Background(), 0, nil, nil, false)
	// FIN has left the sender, but its packet has not been acknowledged.
	str.finishedWriting = true
	str.finSent = true
	str.numOutstandingFrames = 1
	want := errors.New("connection aborted before peer ACK")
	str.closeForShutdown(want)
	if err := (&Stream{sendStr: str}).WaitWriteAcknowledged(t.Context()); !errors.Is(err, want) {
		t.Fatalf("unacknowledged shutdown must fail, got %v", err)
	}
}
