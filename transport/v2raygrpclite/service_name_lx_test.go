package v2raygrpclite

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/grpcname"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck
)

// lx: SPEC 093 — Xray's `service_name` forms, on the wire and end to end.

// echoHandler accepts the server side of a lite gRPC stream and echoes it back.
type echoHandler struct {
	accepted chan struct{}
}

func (h *echoHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	select {
	case h.accepted <- struct{}{}:
	default:
	}
	go func() {
		io.Copy(conn, conn)
		conn.Close()
		if onClose != nil {
			onClose(nil)
		}
	}()
}

type testDialer struct {
	target string
}

func (d *testDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, d.target)
}

func (d *testDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrUnexpectedEOF
}

// newStand brings up a lite server on loopback (h2c, no TLS) with serverName and
// returns a lite client configured with clientName pointed at it.
func newStand(t *testing.T, serverName, clientName string) (adapter.V2RayClientTransport, *echoHandler) {
	t.Helper()
	ctx := context.Background()
	handler := &echoHandler{accepted: make(chan struct{}, 1)}
	server, err := NewServer(ctx, logger.NOP(), option.V2RayGRPCOptions{ServiceName: serverName}, nil, handler)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
	})
	serverAddr := M.ParseSocksaddr(listener.Addr().String())
	client := NewClient(ctx, &testDialer{target: listener.Addr().String()}, serverAddr, option.V2RayGRPCOptions{ServiceName: clientName}, nil)
	t.Cleanup(func() {
		client.Close()
	})
	return client, handler
}

// roundTrip dials through the client and pushes one payload through the stream.
func roundTrip(t *testing.T, client adapter.V2RayClientTransport) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	payload := []byte("lx-spec-093")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buffer := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, buffer); err != nil {
		return err
	}
	if string(buffer) != string(payload) {
		t.Fatalf("echo mismatch: %q", buffer)
	}
	return nil
}

func TestServiceNameFormsMatch(t *testing.T) {
	for _, serviceName := range []string{"TunService", "a/b", "/a/b/Tun", "/a/b/Stream"} {
		t.Run(serviceName, func(t *testing.T) {
			client, handler := newStand(t, serviceName, serviceName)
			if err := roundTrip(t, client); err != nil {
				t.Fatalf("service_name %q did not connect: %v", serviceName, err)
			}
			select {
			case <-handler.accepted:
			default:
				t.Fatal("server never accepted the connection")
			}
		})
	}
}

// TestXrayPathInterop is the red-check that the lite<->lite stand cannot give:
// both sides shared the same wrong path before SPEC 093, so they agreed anyway.
// Here the server is a bare listener that only accepts the path Xray computes,
// which is what a real Xray server compares against.
func TestXrayPathInterop(t *testing.T) {
	for _, serviceName := range []string{"TunService", "a/b", "/a/b/Tun", "/a/b/Stream"} {
		t.Run(serviceName, func(t *testing.T) {
			got := clientWirePath(t, serviceName)
			if want := grpcname.RawPath(serviceName); got != want {
				t.Fatalf("service_name %q: Xray expects :path %q, client sent %q", serviceName, want, got)
			}
		})
	}
}

func TestServiceNameFormsCross(t *testing.T) {
	// The server compares the decoded path (unchanged behaviour), so it is more
	// lenient than Xray: "a/b" also accepts a client that sends "/a/b/Tun"
	// unescaped — the with_grpc client does that for the old form. A different
	// stream name is still a different path.
	client, _ := newStand(t, "a/b", "/a/b/Tun")
	if err := roundTrip(t, client); err != nil {
		t.Fatalf("unescaped /a/b/Tun must still reach server a/b: %v", err)
	}
	client, _ = newStand(t, "/a/b/Tun", "/a/b/Stream")
	if err := roundTrip(t, client); err == nil {
		t.Fatal("client /a/b/Stream unexpectedly reached server /a/b/Tun")
	}
}

// TestWirePath pins the :path each form puts on the wire. The h2 transport
// serializes url.URL via EscapedPath(), which only honours RawPath when it is a
// valid encoding of Path — so this asserts what the peer actually sees.
func TestWirePath(t *testing.T) {
	testCases := map[string]string{
		"TunService":        "/TunService/Tun",
		"a/b":               "/a%2Fb/Tun",
		"/a/b/Tun":          "/a/b/Tun",
		"/a/b/Stream":       "/a/b/Stream",
		"/a b/Tun":          "/a%20b/Tun",
		"/a/b/Tun|TunMulti": "/a/b/Tun",
		"/Tun":              "//Tun", // Xray edge case: empty service name
	}
	for serviceName, want := range testCases {
		t.Run(serviceName, func(t *testing.T) {
			got := clientWirePath(t, serviceName)
			t.Logf("service_name %q -> :path %q", serviceName, got)
			if got != want {
				t.Fatalf("service_name %q: wire path = %q, want %q", serviceName, got, want)
			}
		})
	}
}

// clientWirePath runs one dial against a bare HTTP/2 listener that records the
// request path, and returns the :path the client sent.
func clientWirePath(t *testing.T, serviceName string) string {
	t.Helper()
	paths := make(chan string, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case paths <- request.URL.EscapedPath():
		default:
		}
		writer.WriteHeader(http.StatusNotFound)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h2Server := &http2.Server{}
	httpServer := &http.Server{Handler: h2c.NewHandler(handler, h2Server)} //nolint:staticcheck
	go httpServer.Serve(listener)
	t.Cleanup(func() {
		httpServer.Close()
	})
	ctx := context.Background()
	client := NewClient(ctx, &testDialer{target: listener.Addr().String()}, M.ParseSocksaddr(listener.Addr().String()), option.V2RayGRPCOptions{ServiceName: serviceName}, nil)
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := client.DialContext(dialCtx)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("x"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	conn.Read(make([]byte, 1))
	conn.Close()
	select {
	case path := <-paths:
		return path
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the listener")
		return ""
	}
}

// TestOldFormUnchanged is the guard for SPEC 093 §5: without a leading "/" the
// Path/RawPath pair must stay byte for byte what it was before the change.
func TestOldFormUnchanged(t *testing.T) {
	for _, serviceName := range []string{"TunService", "a/b", "с пробелом ", "%"} {
		client := NewClient(context.Background(), &testDialer{target: "127.0.0.1:1"}, M.ParseSocksaddr("127.0.0.1:1"), option.V2RayGRPCOptions{ServiceName: serviceName}, nil).(*Client)
		wantPath := "/" + serviceName + "/Tun"
		wantRawPath := "/" + url.PathEscape(serviceName) + "/Tun"
		if client.url.Path != wantPath {
			t.Fatalf("service_name %q: Path = %q, want %q", serviceName, client.url.Path, wantPath)
		}
		if client.url.RawPath != wantRawPath {
			t.Fatalf("service_name %q: RawPath = %q, want %q", serviceName, client.url.RawPath, wantRawPath)
		}
		client.Close()
	}
}
