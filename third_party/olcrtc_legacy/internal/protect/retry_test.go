package protect

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
)

// TestIsRetriableErrorOnPeerReset is the regression guard for the failure that
// broke olcRTC against a flaky Jitsi host: the peer reset arrives as a
// *net.OpError, and classifying OpError by timeout alone made a single reset
// fatal.
func TestIsRetriableErrorOnPeerReset(t *testing.T) {
	reset := &net.OpError{
		Op:     "read",
		Net:    "tcp",
		Source: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 66), Port: 54792},
		Addr:   &net.TCPAddr{IP: net.IPv4(213, 59, 255, 176), Port: 443},
		Err:    &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET},
	}
	if !isRetriableError(reset) {
		t.Fatalf("isRetriableError(%v) = false, want true", reset)
	}
}

func TestIsRetriableErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"dns", &net.DNSError{Err: "no such host", IsNotFound: true}, true},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"reset text", errors.New("read: connection reset by peer"), true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"permanent", errors.New("x509: certificate signed by unknown authority"), false},
	} {
		if got := isRetriableError(tc.err); got != tc.want {
			t.Fatalf("%s: isRetriableError = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestRetryTransportRecoversFromReset exercises the whole client: the first
// answer is a reset, the retry must still deliver the body.
func TestRetryTransportRecoversFromReset(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					if tcpConn, ok := conn.(*net.TCPConn); ok {
						_ = tcpConn.SetLinger(0) // close with RST
					}
					_ = conn.Close()
					return
				}
			}
		}
		_, _ = w.Write([]byte("config"))
	}))
	defer server.Close()

	resp, err := NewHTTPClient().Get(server.URL)
	if err != nil {
		t.Fatalf("Get = %v, want the retry to succeed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if attempts < 2 {
		t.Fatalf("attempts = %d, want the request to be retried", attempts)
	}
}
