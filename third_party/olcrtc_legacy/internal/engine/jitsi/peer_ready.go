package jitsi

import (
	"context"
	"fmt"
	"time"
)

// WaitForPeer waits for an incoming bridge frame before starting a reliable
// stream. A locally open SCTP channel does not mean JVB can forward to the
// remote endpoint yet; an early smux SYN can otherwise be lost permanently.
func (s *Session) WaitForPeer(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.peerEpoch.Load() != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for remote bridge: %w", ctx.Err())
		case <-s.done:
			return ErrSessionClosed
		case <-ticker.C:
		}
	}
}
