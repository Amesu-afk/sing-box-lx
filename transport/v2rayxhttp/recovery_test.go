package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Real HTTP/2 streams with a TCP wrapper that silently loses outgoing bytes,
// reproducing a dropped NAT mapping without a FIN/RST or an Android device.
type recoverySocket struct {
	net.Conn
	dropWrites atomic.Bool
	closed     atomic.Bool
}

func (c *recoverySocket) Write(p []byte) (int, error) {
	if c.dropWrites.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *recoverySocket) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

type recoveryDialer struct {
	mu      sync.Mutex
	sockets []*recoverySocket
}

func (d *recoveryDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	socket := &recoverySocket{Conn: conn}
	d.mu.Lock()
	d.sockets = append(d.sockets, socket)
	d.mu.Unlock()
	return socket, nil
}

func (d *recoveryDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func (d *recoveryDialer) snapshot() []*recoverySocket {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*recoverySocket(nil), d.sockets...)
}

func recoveryClient(t *testing.T, poolSize int) (*Client, *recoveryDialer) {
	t.Helper()
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{'R'})
		w.(http.Flusher).Flush()
		buffer := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buffer)
			if n > 0 {
				_, _ = w.Write(buffer[:n])
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	}), &http2.Server{}))
	dialer := &recoveryDialer{}
	transport, err := NewClient(context.Background(), dialer, M.ParseSocksaddr(server.Listener.Addr().String()), option.V2RayXHTTPOptions{Mode: modeStreamOne, Path: "/recovery/"}, nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	client := transport.(*Client)
	client.slots = client.slots[:poolSize]
	t.Cleanup(func() {
		_ = client.Close()
		for _, socket := range dialer.snapshot() {
			_ = socket.Close()
		}
		server.Close()
	})
	return client, dialer
}

func recoveryStream(t *testing.T, client *Client) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var greeting [1]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 'R' {
		t.Fatalf("missing greeting: %q, %v", greeting, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn
}

func recoveryRead(conn net.Conn) <-chan error {
	done := make(chan error, 1)
	go func() {
		var data [1]byte
		_, err := conn.Read(data[:])
		done <- err
	}()
	return done
}

func requireRecoveryReadClosed(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stream read succeeded after socket reset")
		}
	case <-time.After(time.Second):
		t.Fatal("stream read remained blocked after socket reset")
	}
}

func TestHTTP2ResetClosesActiveStreamsAndReusesClient(t *testing.T) {
	client, dialer := recoveryClient(t, 1)
	stream := recoveryStream(t, client)
	done := recoveryRead(stream)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !dialer.snapshot()[0].closed.Load() {
		t.Fatal("reset left active TCP socket open")
	}
	requireRecoveryReadClosed(t, done)
	_ = recoveryStream(t, client)
	if len(dialer.snapshot()) != 2 {
		t.Fatal("reused client did not establish a fresh socket")
	}
}

func TestHTTP2WakeResetAvoidsWarmBlackholeTimeout(t *testing.T) {
	client, dialer := recoveryClient(t, 1)
	stream := recoveryStream(t, client)
	dialer.snapshot()[0].dropWrites.Store(true)
	_, _ = stream.Write([]byte{'X'})
	done := recoveryRead(stream)
	start := time.Now()
	_ = client.Close() // The native network-reset path used on Android wake.
	requireRecoveryReadClosed(t, done)
	_ = recoveryStream(t, client)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("first fresh stream took %v after wake reset", elapsed)
	} else {
		t.Logf("first post-reset stream answered in %v", elapsed)
	}
}

func TestHTTP2RetirementReapsSilentSibling(t *testing.T) {
	defer swapHandshakeTimeout(100 * time.Millisecond)()
	client, dialer := recoveryClient(t, 2)
	_ = recoveryStream(t, client)
	sibling := recoveryStream(t, client)
	failed := client.slots[0].get()
	for _, socket := range dialer.snapshot() {
		socket.dropWrites.Store(true)
	}
	time.Sleep(150 * time.Millisecond)
	done := recoveryRead(sibling)
	client.retireTransport(failed)
	for index, socket := range dialer.snapshot() {
		if !socket.closed.Load() {
			t.Fatalf("silent socket %d survived retirement", index)
		}
	}
	requireRecoveryReadClosed(t, done)
	_ = recoveryStream(t, client)
}

func TestHTTP2RetirementPreservesReceivingSiblingUntilReset(t *testing.T) {
	defer swapHandshakeTimeout(100 * time.Millisecond)()
	client, dialer := recoveryClient(t, 2)
	_ = recoveryStream(t, client)
	sibling := recoveryStream(t, client)
	failed := client.slots[0].get()
	original := dialer.snapshot()
	original[0].dropWrites.Store(true)
	time.Sleep(150 * time.Millisecond)
	if _, err := sibling.Write([]byte{'E'}); err != nil {
		t.Fatal(err)
	}
	_ = sibling.SetReadDeadline(time.Now().Add(time.Second))
	var echo [1]byte
	if _, err := io.ReadFull(sibling, echo[:]); err != nil || echo[0] != 'E' {
		t.Fatalf("receiving sibling failed: %q, %v", echo, err)
	}
	client.retireTransport(failed)
	if !original[0].closed.Load() || original[1].closed.Load() {
		t.Fatal("retirement did not distinguish stale and receiving sockets")
	}
	_ = client.Close()
	if !original[1].closed.Load() {
		t.Fatal("explicit reset lost ownership of the replaced receiving member")
	}
}

func TestResetRejectsDialCompletingOnOldGeneration(t *testing.T) {
	client := newTestClient(1)
	old := client.slots[0].get()
	_ = client.Close()
	local, peer := net.Pipe()
	defer peer.Close()
	tracked := old.trackConnection(local) // A slow TCP/TLS dial returned after reset.
	if _, err := tracked.Write([]byte{'X'}); err == nil {
		t.Fatal("late old-network socket remained open")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example/", nil)
	if _, err := old.RoundTrip(req); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("captured old member accepted a new request: %v", err)
	}
	if len(client.connections.snapshot(false)) != 0 {
		t.Fatal("rejected socket remained in the ownership registry")
	}
}

func TestRetirementPreservesColdSiblingHandshake(t *testing.T) {
	client := newTestClient(2)
	local, peer := net.Pipe()
	defer peer.Close()
	tracked := client.slots[1].get().trackConnection(local)
	defer tracked.Close()
	client.retireTransport(client.slots[0].get())
	_ = tracked.SetWriteDeadline(time.Now().Add(time.Second))
	go func() { _, _ = peer.Read(make([]byte, 1)) }()
	if _, err := tracked.Write([]byte{'X'}); err != nil {
		t.Fatalf("cold sibling handshake interrupted: %v", err)
	}
}

func TestResetRacingSocketRegistrationDoesNotLeakOldGeneration(t *testing.T) {
	client := newTestClient(1)
	old := client.slots[0].get()
	start := make(chan struct{})
	sockets := make(chan *recoverySocket, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			local, peer := net.Pipe()
			defer peer.Close()
			socket := &recoverySocket{Conn: local}
			<-start
			old.trackConnection(socket)
			sockets <- socket
		}()
	}
	close(start)
	_ = client.Close()
	workers.Wait()
	close(sockets)
	for socket := range sockets {
		if !socket.closed.Load() {
			t.Fatal("socket from an old generation escaped a concurrent reset")
		}
	}
	if len(client.connections.snapshot(false)) != 0 {
		t.Fatal("old generation remained in the socket registry")
	}
}
