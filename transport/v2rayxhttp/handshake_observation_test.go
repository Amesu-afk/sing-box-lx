package v2rayxhttp

import (
	"context"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTP2ObservationIdentifiesReusedConnection(t *testing.T) {
	client, _ := recoveryClient(t, 1)
	_ = recoveryStream(t, client)
	req, err := client.newRequest(context.Background(), http.MethodGet, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	req, observation := observeHandshake(req)
	member := client.slots[0].get()
	resp, err := handshakeRoundTrip(member, req, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	state := observation.describe(member)
	for _, want := range []string{"phase=response-started", "reused=true", "sockets=1"} {
		if !strings.Contains(state, want) {
			t.Fatalf("missing %q in actual HTTP/2 observation: %s", want, state)
		}
	}
}

func TestHTTP2TimedOutRequestPreservesReceivingStreamOnSameSocket(t *testing.T) {
	defer swapHandshakeTimeout(80 * time.Millisecond)()
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{'R'})
		w.(http.Flusher).Flush()
		b := make([]byte, 1024)
		for {
			n, err := r.Body.Read(b)
			if n > 0 {
				_, _ = w.Write(b[:n])
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	dialer := &recoveryDialer{}
	transport, err := NewClient(context.Background(), dialer, M.ParseSocksaddr(server.Listener.Addr().String()), option.V2RayXHTTPOptions{Mode: modeStreamOne, Path: "/echo/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	client.slots = client.slots[:1]
	t.Cleanup(func() { _ = client.Close() })
	stream := recoveryStream(t, client)
	member := client.slots[0].get()
	req, _ := client.newRequest(context.Background(), http.MethodGet, "", "", nil)
	req.URL.Path = "/slow"
	done := make(chan error, 1)
	go func() { _, err := client.handshakeVia(member, req); done <- err }()
	echo := func() {
		t.Helper()
		_ = stream.SetDeadline(time.Now().Add(time.Second))
		if _, err := stream.Write([]byte{'H'}); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err := io.ReadFull(stream, b[:]); err != nil || b[0] != 'H' {
			t.Fatalf("healthy sibling failed: %v", err)
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	waiting := true
	for waiting {
		select {
		case err := <-done:
			if err != errHandshakeTimeout {
				t.Fatalf("expected timed-out headers, got %v", err)
			}
			waiting = false
		case <-tick.C:
			echo()
		case <-deadline.C:
			t.Fatal("header timeout did not return")
		}
	}
	client.retireTransport(member)
	if len(dialer.snapshot()) != 1 || dialer.snapshot()[0].closed.Load() {
		t.Fatal("timed-out request closed its receiving socket sibling")
	}
	echo()
}

func TestClosingOneOfTwoPhysicalConnectionsKeepsMemberWarm(t *testing.T) {
	member := &poolTransport{}
	first, firstPeer := net.Pipe()
	second, secondPeer := net.Pipe()
	defer firstPeer.Close()
	defer secondPeer.Close()
	a, b := member.trackConnection(first), member.trackConnection(second)
	defer b.Close()
	member.warm.Store(true)
	_ = a.Close()
	if !member.warm.Load() {
		t.Fatal("closing one socket made a still-connected member cold")
	}
}
