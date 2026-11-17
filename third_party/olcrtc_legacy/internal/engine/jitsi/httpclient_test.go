package jitsi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingTransport struct {
	got *http.Request
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.got = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// TestBrowserHeaderTransportFillsUserAgent covers the WAF case: a request that
// carries no User-Agent must reach the network looking like a browser, because
// hosts fronted by an anti-bot filter reset a plain Go request.
func TestBrowserHeaderTransportFillsUserAgent(t *testing.T) {
	base := &recordingTransport{}
	client := &http.Client{Transport: &browserHeaderTransport{base: base}}

	req := httptest.NewRequest(http.MethodGet, "https://meet.example.com/config.js", nil)
	req.RequestURI = ""
	if _, err := client.Do(req); err != nil {
		t.Fatalf("client.Do = %v, want nil", err)
	}
	if got := base.got.Header.Get("User-Agent"); got != browserUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, browserUserAgent)
	}
	if got := base.got.Header.Get("Accept"); got == "" {
		t.Fatal("Accept header not set")
	}
}

// TestBrowserHeaderTransportKeepsCallerHeaders makes sure the wrapper never
// overwrites the headers j sets itself on the WebSocket handshake.
func TestBrowserHeaderTransportKeepsCallerHeaders(t *testing.T) {
	base := &recordingTransport{}
	client := &http.Client{Transport: &browserHeaderTransport{base: base}}

	req := httptest.NewRequest(http.MethodGet, "https://meet.example.com/xmpp-websocket", nil)
	req.RequestURI = ""
	req.Header.Set("User-Agent", "caller/1.0")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("client.Do = %v, want nil", err)
	}
	if got := base.got.Header.Get("User-Agent"); got != "caller/1.0" {
		t.Fatalf("User-Agent = %q, want the caller's own value", got)
	}
}

// TestNewProtectedHTTPClientHasJar guards the session cookie a WAF hands out on
// /config.js being carried over to the WebSocket handshake.
func TestNewProtectedHTTPClientHasJar(t *testing.T) {
	if client := newProtectedHTTPClient(false); client.Jar == nil {
		t.Fatal("newProtectedHTTPClient returned a client without a cookie jar")
	}
}
