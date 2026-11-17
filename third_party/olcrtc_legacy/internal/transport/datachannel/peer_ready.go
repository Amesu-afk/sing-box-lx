package datachannel

import "context"

// WaitForPeer forwards remote readiness when the carrier can observe it.
func (p *streamTransport) WaitForPeer(ctx context.Context) error {
	if waiter, ok := p.session.(interface{ WaitForPeer(context.Context) error }); ok {
		return waiter.WaitForPeer(ctx)
	}
	return nil
}
