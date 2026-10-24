package v2rayxhttp

import (
	"context"
	"net/http"
	"time"
)

type headerRoundTripResult struct {
	response *http.Response
	err      error
}

// boundedHeaderRoundTrip must not rely on RoundTrip reacting to cancellation:
// HTTP/2 can wait for a shared dial whose initial preface write is still blocked.
// Only this request is cancelled. The caller's existing retirement path owns
// physical socket cleanup, preserving healthy multiplexed siblings.
func boundedHeaderRoundTrip(rt http.RoundTripper, req *http.Request, timeout time.Duration) (*http.Response, error) {
	parent := req.Context()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	stopParent := context.AfterFunc(parent, cancel)
	defer stopParent()
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	results := make(chan headerRoundTripResult)
	go func() {
		resp, err := rt.RoundTrip(req.WithContext(ctx))
		select {
		case results <- headerRoundTripResult{resp, err}:
			// The unbuffered handoff transfers body ownership to the caller.
		case <-ctx.Done():
			closeHeaderResponse(resp)
		}
	}()
	select {
	case result := <-results:
		if !stopParent() {
			cancel()
			discardHeaderResponse(result.response)
			return nil, parent.Err()
		}
		if !time.Now().Before(deadline) {
			cancel()
			discardHeaderResponse(result.response)
			return nil, errHandshakeTimeout
		}
		if result.err != nil {
			cancel()
			discardHeaderResponse(result.response)
			return nil, result.err
		}
		result.response.Body = &cancelBody{ReadCloser: result.response.Body, cancel: cancel}
		return result.response, nil
	case <-timer.C:
		cancel()
		return nil, errHandshakeTimeout
	case <-ctx.Done():
		cancel()
		return nil, parent.Err()
	}
}

// A discarded response can itself be waiting for HTTP/2 cleanup. That must not
// extend an expired opening budget or an already-acknowledged packet Write.
func discardHeaderResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		go closeHeaderResponse(resp)
	}
}

func closeHeaderResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
