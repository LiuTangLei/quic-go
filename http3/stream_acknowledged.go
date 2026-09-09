package http3

import "context"

// WaitWriteAcknowledged waits for all DATA bytes and FIN, not merely local Close.
func (s *Stream) WaitWriteAcknowledged(ctx context.Context) error {
	return s.QUICStream().WaitWriteAcknowledged(ctx)
}

func (s *RequestStream) WaitWriteAcknowledged(ctx context.Context) error {
	return s.str.WaitWriteAcknowledged(ctx)
}
