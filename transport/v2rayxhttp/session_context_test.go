package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func sessionContextClient(t *testing.T, rt http.RoundTripper, mode string) *Client {
	t.Helper()
	c := newTestClient(1)
	c.slots[0].get().rt = rt
	c.scheme, c.host, c.path = "http", "session.test", "/session/"
	c.serverAddr = M.ParseSocksaddr("session.test:80")
	var err error
	c.meta, err = normalizeMeta(metaOptions{}, mode)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStreamUpUploadFailureUnblocksDownload(t *testing.T) {
	down, downWriter := io.Pipe()
	defer downWriter.Close()
	release := make(chan struct{})
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			<-release
			return nil, errors.New("upload failed")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: down}, nil
	})
	c := sessionContextClient(t, rt, modeStreamUp)
	conn, err := c.dialStreamUp(context.Background(), "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := recoveryRead(conn)
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed session read succeeded")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("upload failed but download kept waiting")
	}
	if c.slots[0].get().inflight.Load() != 0 {
		t.Fatal("failed session kept its pool lease")
	}
}

func TestStreamOneHandshakeTimeoutUnblocksPendingWrite(t *testing.T) {
	defer swapFreshHandshakeTimeout(30 * time.Millisecond)()
	c, rt := hangingClient(t)
	c.newTransport = func() *poolTransport {
		return &poolTransport{rt: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF })}
	}
	t.Cleanup(func() { _ = rt.Close() })
	conn, err := c.dialStreamOne(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { _, err := conn.Write([]byte("VLESS handshake")); done <- err }()
	select {
	case err := <-done:
		if err != errHandshakeTimeout {
			t.Fatalf("got %v, want handshake timeout", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("handshake timed out but pending protocol write remained blocked")
	}
}

func TestPacketUpSurvivesDialContextCancellation(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	c := sessionContextClient(t, rt, modePacketUp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := c.dialPacketUp(ctx, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel() // Dial succeeded; its temporary context has finished its job.
	if _, err := conn.Write([]byte("established session")); err != nil {
		t.Fatalf("established packet-up upload inherited cancelled dial context: %v", err)
	}
}

func TestPacketUpAckDoesNotWaitForResponseBodyEOF(t *testing.T) {
	ack, ackWriter := io.Pipe()
	defer ack.Close()
	defer ackWriter.Close()
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := io.ReadCloser(io.NopCloser(strings.NewReader("")))
		if req.Method != http.MethodGet {
			body = ack
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	c := sessionContextClient(t, rt, modePacketUp)
	conn, err := c.dialPacketUp(context.Background(), "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { _, err := conn.Write([]byte("already acknowledged")); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("successful packet ack waited for a response body that never ends")
	}
}

type blockedCleanupBody struct{ release <-chan struct{} }

func (*blockedCleanupBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *blockedCleanupBody) Close() error           { <-b.release; return nil }

func TestRejectedResponseDoesNotBlockOpeningError(t *testing.T) {
	for _, mode := range []string{modeStreamOne, modePacketUp} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: &blockedCleanupBody{release: release}}, nil
			})
			c := sessionContextClient(t, rt, mode)
			done := make(chan error, 1)
			go func() {
				if mode == modeStreamOne {
					conn, err := c.dialStreamOne(context.Background(), "")
					if err == nil {
						defer conn.Close()
						_, err = conn.Read(make([]byte, 1))
					}
					done <- err
				} else {
					_, _, err := c.dialDownloadStream(context.Background(), "session")
					done <- err
				}
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("rejected request succeeded")
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("response cleanup hid the opening error")
			}
		})
	}
}

func TestStreamUpOwnsUploadUntilConnectionClose(t *testing.T) {
	uploadCtx := make(chan context.Context, 1)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			uploadCtx <- req.Context()
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	c := sessionContextClient(t, rt, modeStreamUp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := c.dialStreamUp(ctx, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var live context.Context
	select {
	case live = <-uploadCtx:
	case <-time.After(time.Second):
		t.Fatal("upload did not start")
	}
	cancel()
	select {
	case <-live.Done():
		t.Fatal("established stream-up upload inherited cancelled dial context")
	case <-time.After(20 * time.Millisecond):
	}
	_ = conn.Close()
	select {
	case <-live.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel upload")
	}
}
