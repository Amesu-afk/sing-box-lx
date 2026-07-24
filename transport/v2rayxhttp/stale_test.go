package v2rayxhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// roundTripFunc adapts a function to http.RoundTripper for the stale-connection
// tests. It stands in for a pooled *http2.Transport without a real server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A pooled connection gone stale never produces response headers. handshakeRoundTrip
// must give up after handshakeTimeout, cancel just this request (so the blocked
// RoundTrip unwinds via its context), and report errHandshakeTimeout.
func TestHandshakeRoundTripTimesOutAndCancels(t *testing.T) {
	defer swapHandshakeTimeout(30 * time.Millisecond)()

	observed := make(chan struct{})
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done() // a zombie conn: block until the timeout cancels us
		close(observed)
		return nil, req.Context().Err()
	})

	req, err := http.NewRequest(http.MethodGet, "https://example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handshakeRoundTrip(rt, req); err != errHandshakeTimeout {
		t.Fatalf("got %v, want errHandshakeTimeout", err)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("request context was not cancelled on handshake timeout")
	}
}

// Once headers arrive, the timer's job is done: the stream context must stay live
// long past the handshake window (so a long-lived relay is never cut), and only
// closing the body releases it.
func TestHandshakeRoundTripSuccessKeepsStreamAlive(t *testing.T) {
	defer swapHandshakeTimeout(20 * time.Millisecond)()

	var streamCtx context.Context
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamCtx = req.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("payload")),
		}, nil
	})

	req, err := http.NewRequest(http.MethodGet, "https://example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := handshakeRoundTrip(rt, req)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(4 * handshakeTimeout) // well past the header deadline
	if streamCtx.Err() != nil {
		t.Fatalf("stream context cancelled after headers arrived: %v", streamCtx.Err())
	}

	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if streamCtx.Err() == nil {
		t.Fatal("stream context not released on body close (context leak)")
	}
}

// A real error from the transport (not our timeout) must pass through unchanged.
func TestHandshakeRoundTripPassesThroughRealError(t *testing.T) {
	defer swapHandshakeTimeout(time.Second)()

	want := io.ErrUnexpectedEOF
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, want })
	req, _ := http.NewRequest(http.MethodGet, "https://example/", nil)
	if _, err := handshakeRoundTrip(rt, req); err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

// refreshAllSlots swaps every member for a fresh transport, and a second call
// inside the debounce window is a no-op (so the burst of stalled initial dials
// after wake rebuilds the pool exactly once).
func TestRefreshAllSlotsSwapsThenDebounces(t *testing.T) {
	c := &Client{newTransport: func() *http2.Transport { return &http2.Transport{} }}
	c.slots = make([]*transportSlot, 3)
	for i := range c.slots {
		s := &transportSlot{}
		s.ptr.Store(c.newTransport())
		c.slots[i] = s
	}

	before := make([]*http2.Transport, len(c.slots))
	for i, s := range c.slots {
		before[i] = s.get()
	}

	c.refreshAllSlots()
	for i, s := range c.slots {
		if s.get() == before[i] {
			t.Fatalf("slot %d was not swapped by refreshAllSlots", i)
		}
	}

	afterFirst := make([]*http2.Transport, len(c.slots))
	for i, s := range c.slots {
		afterFirst[i] = s.get()
	}
	c.refreshAllSlots() // within slotRefreshDebounce → must not swap again
	for i, s := range c.slots {
		if s.get() != afterFirst[i] {
			t.Fatalf("slot %d swapped again inside the debounce window", i)
		}
	}
}

func swapHandshakeTimeout(d time.Duration) func() {
	old := handshakeTimeout
	handshakeTimeout = d
	return func() { handshakeTimeout = old }
}
