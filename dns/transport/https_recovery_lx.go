//go:build with_xhttp

package transport

import (
	"context"

	mDNS "github.com/miekg/dns"
)

// A wake reset can close the DoH socket while an application's first query is
// already in flight. DNS queries are replayable; streamed XHTTP uploads are not.
// Retry once only after an observed reset and within the caller's original budget.
func (t *HTTPSTransport) recoveryTransport() *HTTPSTransportWrapper {
	t.transportAccess.Lock()
	defer t.transportAccess.Unlock()
	return t.transport
}

func (t *HTTPSTransport) exchangeAfterNetworkReset(ctx context.Context, message *mDNS.Msg, initialTransport *HTTPSTransportWrapper, originalErr error) (*mDNS.Msg, error) {
	if ctx.Err() != nil {
		return nil, originalErr
	}
	if t.recoveryTransport() == initialTransport {
		return nil, originalErr
	}
	return t.exchange(ctx, message)
}
