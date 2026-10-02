//go:build with_xhttp

package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	boxDNS "github.com/sagernet/sing-box/dns"
	M "github.com/sagernet/sing/common/metadata"

	mDNS "github.com/miekg/dns"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type recoveryDoHSocket struct {
	net.Conn
	dropWrites atomic.Bool
	closed     atomic.Bool
	dropped    chan struct{}
}

func (c *recoveryDoHSocket) Write(p []byte) (int, error) {
	if c.dropWrites.Load() {
		select {
		case c.dropped <- struct{}{}:
		default:
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *recoveryDoHSocket) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

type recoveryDoHDialer struct {
	mu      sync.Mutex
	sockets []*recoveryDoHSocket
}

func (d *recoveryDoHDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	socket := &recoveryDoHSocket{Conn: conn, dropped: make(chan struct{}, 1)}
	d.mu.Lock()
	d.sockets = append(d.sockets, socket)
	d.mu.Unlock()
	return socket, nil
}

func (d *recoveryDoHDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func (d *recoveryDoHDialer) snapshot() []*recoveryDoHSocket {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*recoveryDoHSocket(nil), d.sockets...)
}

func recoveryDoHTransport(t *testing.T) (*HTTPSTransport, *recoveryDoHDialer, *mDNS.Msg) {
	t.Helper()
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		var question mDNS.Msg
		if err := question.Unpack(payload); err != nil {
			t.Errorf("unpack question: %v", err)
			return
		}
		var answer mDNS.Msg
		answer.SetReply(&question)
		encoded, _ := answer.Pack()
		w.Header().Set("Content-Type", MimeType)
		_, _ = w.Write(encoded)
	}), &http2.Server{}))
	dialer := &recoveryDoHDialer{}
	destination, _ := url.Parse("https://" + server.Listener.Addr().String() + "/dns-query")
	// The production custom dialer speaks plaintext HTTP/2 under this local URL.
	transport := NewHTTPSRaw(boxDNS.NewTransportAdapter("https", "recovery", nil), nil, dialer, destination, make(http.Header), M.ParseSocksaddr(server.Listener.Addr().String()), nil)
	t.Cleanup(func() {
		_ = transport.Close()
		server.Close()
	})
	question := new(mDNS.Msg)
	question.SetQuestion("recovery.invalid.", mDNS.TypeA)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := transport.Exchange(ctx, question); err != nil {
		t.Fatal(err)
	}
	return transport, dialer, question
}

func TestHTTP2DoHQueryCrossingWakeResetRetriesWithinOriginalBudget(t *testing.T) {
	transport, dialer, question := recoveryDoHTransport(t)
	old := dialer.snapshot()[0]
	old.dropWrites.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		answer, err := transport.Exchange(ctx, question)
		if err == nil && (answer == nil || !answer.Response) {
			err = errors.New("missing DNS response")
		}
		done <- err
	}()
	select {
	case <-old.dropped:
	case <-time.After(time.Second):
		t.Fatal("query did not reach the stale HTTP/2 socket")
	}
	transport.Reset()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first DNS query failed instead of retrying after reset: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("DNS query did not recover inside its original budget")
	}
	if !old.closed.Load() || len(dialer.snapshot()) != 2 {
		t.Fatal("retry did not replace the old DoH socket exactly once")
	}
}

func TestHTTP2DoHExpiredBudgetIsNotExtendedOrReplayed(t *testing.T) {
	transport, dialer, question := recoveryDoHTransport(t)
	old := dialer.snapshot()[0]
	old.dropWrites.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := transport.Exchange(ctx, question)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected caller deadline, got %v", err)
	}
	if time.Since(start) > time.Second || len(dialer.snapshot()) != 1 || !old.closed.Load() {
		t.Fatal("expired query was replayed or retained its old socket")
	}
}
