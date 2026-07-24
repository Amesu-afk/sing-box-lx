package v2rayxhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// roundTripFunc adapts a function to http.RoundTripper for the stale-connection
// tests. It stands in for a pooled *http2.Transport without a real server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A pooled connection gone stale never produces response headers. handshakeRoundTrip
// must give up after its budget, cancel just this request (so the blocked RoundTrip
// unwinds via its context), and report errHandshakeTimeout.
func TestHandshakeRoundTripTimesOutAndCancels(t *testing.T) {
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
	if _, err := handshakeRoundTrip(rt, req, 30*time.Millisecond); err != errHandshakeTimeout {
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
	const budget = 20 * time.Millisecond

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
	resp, err := handshakeRoundTrip(rt, req, budget)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(4 * budget) // well past the header deadline
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
	want := io.ErrUnexpectedEOF
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, want })
	req, _ := http.NewRequest(http.MethodGet, "https://example/", nil)
	if _, err := handshakeRoundTrip(rt, req, time.Second); err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

// A member that has never answered is still owed its TCP + TLS/REALITY + h2 preface,
// so it gets the cold budget; once it has answered, the same member is held to the
// much shorter warm one. Sharing a single deadline is what would fail a slow-but-
// working link on the first dial.
func TestHandshakeViaGivesColdMemberTheLargerBudget(t *testing.T) {
	defer swapHandshakeTimeout(20 * time.Millisecond)()
	defer swapFreshHandshakeTimeout(2 * time.Second)()

	// Slower than the warm budget, comfortably inside the cold one.
	const answerDelay = 150 * time.Millisecond

	c := &Client{}
	pt := &poolTransport{rt: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case <-time.After(answerDelay):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}

	req, _ := http.NewRequest(http.MethodGet, "https://example/", nil)
	resp, err := c.handshakeVia(pt, req)
	if err != nil {
		t.Fatalf("cold dial rejected a link answering in %v: %v", answerDelay, err)
	}
	resp.Body.Close()
	if !pt.warm.Load() {
		t.Fatal("member was not promoted to warm after answering")
	}

	// Same member, same latency — now measured against the warm budget, which it misses.
	req2, _ := http.NewRequest(http.MethodGet, "https://example/", nil)
	if _, err := c.handshakeVia(pt, req2); err != errHandshakeTimeout {
		t.Fatalf("warm dial got %v, want errHandshakeTimeout", err)
	}
}

// refreshAllSlots swaps every member for a fresh transport, and a second call
// inside the debounce window is a no-op (so the burst of stalled initial dials
// after wake rebuilds the pool exactly once).
func TestRefreshAllSlotsSwapsThenDebounces(t *testing.T) {
	c := newTestClient(3)

	before := make([]*poolTransport, len(c.slots))
	for i, s := range c.slots {
		before[i] = s.get()
	}

	c.refreshAllSlots()
	for i, s := range c.slots {
		if s.get() == before[i] {
			t.Fatalf("slot %d was not swapped by refreshAllSlots", i)
		}
	}

	afterFirst := make([]*poolTransport, len(c.slots))
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

// A member rebuilt by a refresh must come back cold, or the dial that follows a
// pool swap — the one that has to redo the whole TLS/REALITY handshake — would be
// judged against the warm budget and fail on exactly the slow link this protects.
func TestRefreshAllSlotsYieldsColdMembers(t *testing.T) {
	c := newTestClient(2)
	for _, s := range c.slots {
		s.get().warm.Store(true)
	}

	c.refreshAllSlots()
	for i, s := range c.slots {
		if s.get().warm.Load() {
			t.Fatalf("slot %d came back warm from refreshAllSlots", i)
		}
	}
}

// recordingTransport is a pool member that counts how often its idle connections
// were closed, standing in for the *http2.Transport whose sockets we care about.
type recordingTransport struct {
	roundTripFunc
	closed atomic.Int32
}

func (t *recordingTransport) CloseIdleConnections() { t.closed.Add(1) }

// The regression this pins: a transport that timed out is NOT closed by the pool
// refresh (at swap time its dying stream still holds the connection, so it is not
// idle), and once swapped out it is unreachable from the slots, so no later
// refresh can ever revisit it. retireTransport must therefore close it directly —
// including when the debounce suppresses the refresh entirely, which is exactly
// the case a sibling dial hits during a post-wake storm.
func TestRetireTransportClosesTimedOutMemberDespiteDebounce(t *testing.T) {
	c := newTestClient(2)

	// Burn the debounce window, so the refresh inside retireTransport is a no-op and
	// only the direct close can account for the connection being reaped.
	c.refreshAllSlots()
	sealed := make([]*poolTransport, len(c.slots))
	for i, s := range c.slots {
		sealed[i] = s.get()
	}

	orphan := &recordingTransport{
		roundTripFunc: func(*http.Request) (*http.Response, error) { return nil, io.EOF },
	}
	c.retireTransport(orphan)

	if got := orphan.closed.Load(); got != 1 {
		t.Fatalf("timed-out transport closed %d times, want exactly 1", got)
	}
	for i, s := range c.slots {
		if s.get() != sealed[i] {
			t.Fatalf("slot %d was swapped inside the debounce window", i)
		}
	}
}

// newTestClient builds a Client with size pool slots and no server behind them.
func newTestClient(size int) *Client {
	c := &Client{newTransport: func() *http2.Transport { return &http2.Transport{} }}
	c.slots = make([]*transportSlot, size)
	for i := range c.slots {
		s := &transportSlot{}
		s.ptr.Store(c.newPoolTransport())
		c.slots[i] = s
	}
	return c
}

func swapHandshakeTimeout(d time.Duration) func() {
	old := handshakeTimeout
	handshakeTimeout = d
	return func() { handshakeTimeout = old }
}

func swapFreshHandshakeTimeout(d time.Duration) func() {
	old := freshHandshakeTimeout
	freshHandshakeTimeout = d
	return func() { freshHandshakeTimeout = old }
}
