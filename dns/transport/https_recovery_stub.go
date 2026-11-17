//go:build !with_xhttp

package transport

import (
	"context"

	mDNS "github.com/miekg/dns"
)

func (t *HTTPSTransport) recoveryTransport() *HTTPSTransportWrapper {
	return nil
}

func (t *HTTPSTransport) exchangeAfterNetworkReset(_ context.Context, _ *mDNS.Msg, _ *HTTPSTransportWrapper, originalErr error) (*mDNS.Msg, error) {
	return nil, originalErr
}
