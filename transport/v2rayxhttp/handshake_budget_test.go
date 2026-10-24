package v2rayxhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type prefaceBlockedConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *prefaceBlockedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(p)
}

// HTTP/2's initial preface write can block before GetClientConn returns.
// A cancelled request context does not itself interrupt that physical write.
func blockedPrefaceTransport(t *testing.T) (*http2.Transport, <-chan struct{}) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	conn := &prefaceBlockedConn{Conn: local, entered: make(chan struct{})}
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(context.Context, string, string, *tls.Config) (net.Conn, error) {
			return conn, nil
		},
	}, conn.entered
}

func TestHandshakeTimeoutBoundsBlockedHTTP2Preface(t *testing.T) {
	rt, entered := blockedPrefaceTransport(t)
	req, _ := http.NewRequest(http.MethodGet, "http://preface.test/", nil)
	done := make(chan error, 1)
	go func() { _, err := handshakeRoundTrip(rt, req, 30*time.Millisecond); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("HTTP/2 did not begin writing its preface")
	}
	select {
	case err := <-done:
		if err != errHandshakeTimeout {
			t.Fatalf("got %v, want handshake timeout", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("handshake budget expired but caller remained blocked in HTTP/2 setup")
	}
}

func TestHandshakeCancellationBoundsBlockedHTTP2Preface(t *testing.T) {
	rt, entered := blockedPrefaceTransport(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://preface.test/", nil)
	done := make(chan error, 1)
	go func() { _, err := handshakeRoundTrip(rt, req, time.Second); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("HTTP/2 did not begin writing its preface")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelled caller remained blocked in HTTP/2 setup")
	}
}

type lateResponseBody struct {
	io.Reader
	closed chan struct{}
}

func (b *lateResponseBody) Close() error { close(b.closed); return nil }

func TestHandshakeTimeoutClosesLateResponse(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	body := &lateResponseBody{Reader: strings.NewReader("late"), closed: make(chan struct{})}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://late.test/", nil)
	done := make(chan error, 1)
	go func() { _, err := handshakeRoundTrip(rt, req, 20*time.Millisecond); done <- err }()
	select {
	case err := <-done:
		if err != errHandshakeTimeout {
			t.Fatalf("got %v, want handshake timeout", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("caller waited for a transport that ignored cancellation")
	}
	// The deferred release lets the late response arrive after caller timeout.
	t.Cleanup(func() {
		select {
		case <-body.closed:
		case <-time.After(time.Second):
			t.Error("late response body leaked")
		}
	})
}

func TestClosedPhysicalConnectionMakesMemberCold(t *testing.T) {
	member := &poolTransport{}
	local, peer := net.Pipe()
	defer peer.Close()
	tracked := member.trackConnection(local)
	member.warm.Store(true)
	_ = tracked.Close()
	if member.warm.Load() {
		t.Fatal("member stayed warm after its last physical connection closed")
	}
}

func TestReconnectingMemberGetsColdHandshakeBudget(t *testing.T) {
	defer swapHandshakeTimeout(20 * time.Millisecond)()
	defer swapFreshHandshakeTimeout(time.Second)()
	member := &poolTransport{rt: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case <-time.After(80 * time.Millisecond):
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	local, peer := net.Pipe()
	defer peer.Close()
	tracked := member.trackConnection(local)
	member.warm.Store(true)
	_ = tracked.Close()
	req, _ := http.NewRequest(http.MethodGet, "https://reconnect.test/", nil)
	resp, err := (&Client{}).handshakeVia(member, req)
	if err != nil {
		t.Fatalf("fresh physical connection was judged by old warm budget: %v", err)
	}
	_ = resp.Body.Close()
}

type contextClosingBody struct {
	ctx     context.Context
	release chan struct{}
}

func (b *contextClosingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *contextClosingBody) Close() error {
	select {
	case <-b.ctx.Done():
	case <-b.release:
	}
	return nil
}

func TestResponseCloseCancelsBeforeWaitingForBody(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &contextClosingBody{ctx: req.Context(), release: release}}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://close.test/", nil)
	resp, err := handshakeRoundTrip(rt, req, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- resp.Body.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Close waited for body cleanup before cancelling the body context")
	}
}
