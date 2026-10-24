package v2rayxhttp

import (
	"fmt"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// One observation belongs to one request, including HTTP/2's internal retries.
// It contains no URL, headers, credentials or payload. Domain logging remains
// at the router's existing debug level; WarnContext supplies the connection ID.
type handshakeObservation struct {
	mu         sync.Mutex
	started    time.Time
	connecting bool
	tcpReady   bool
	connected  bool
	reused     bool
	headers    bool
	firstByte  bool
	connection *trackedPoolConn
	tcpAt      time.Duration
	connAt     time.Duration
	headersAt  time.Duration
	firstAt    time.Duration
}

func observeHandshake(req *http.Request) (*http.Request, *handshakeObservation) {
	o := &handshakeObservation{started: time.Now(), tcpAt: -1, connAt: -1, headersAt: -1, firstAt: -1}
	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			o.mu.Lock()
			o.connected, o.reused, o.headers, o.firstByte = false, false, false, false
			o.connecting, o.tcpReady = false, false
			o.tcpAt, o.connAt, o.headersAt, o.firstAt = -1, -1, -1, -1
			o.connection = nil
			o.mu.Unlock()
		},
		ConnectStart: func(string, string) { o.mu.Lock(); o.connecting = true; o.mu.Unlock() },
		ConnectDone: func(_, _ string, err error) {
			o.mu.Lock()
			if err == nil {
				o.tcpReady = true
				o.tcpAt = time.Since(o.started)
			}
			o.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			o.mu.Lock()
			o.connected, o.reused = true, info.Reused
			o.connAt = time.Since(o.started)
			o.connection, _ = info.Conn.(*trackedPoolConn)
			o.mu.Unlock()
		},
		WroteHeaders:         func() { o.mu.Lock(); o.headers = true; o.headersAt = time.Since(o.started); o.mu.Unlock() },
		GotFirstResponseByte: func() { o.mu.Lock(); o.firstByte = true; o.firstAt = time.Since(o.started); o.mu.Unlock() },
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), o
}

func (o *handshakeObservation) describe(member *poolTransport) string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	phase, reused, conn := "connection-pool", o.reused, o.connection
	switch {
	case o.firstByte:
		phase = "response-started"
	case o.headers:
		phase = "headers-queued"
	case o.connected:
		phase = "request-headers"
	case o.tcpReady:
		phase = "tls-or-h2"
	case o.connecting:
		phase = "tcp-connect"
	}
	tcp, ready, headers, first := traceDuration(o.tcpAt), traceDuration(o.connAt), traceDuration(o.headersAt), traceDuration(o.firstAt)
	o.mu.Unlock()
	member.connectionsMu.Lock()
	sockets := len(member.activeConnections)
	member.connectionsMu.Unlock()
	lastRead := "unknown"
	if conn != nil {
		if last := conn.lastRead.Load(); last != 0 {
			lastRead = time.Since(time.Unix(0, last)).Round(time.Millisecond).String()
		} else {
			lastRead = "never"
		}
	}
	// WroteHeaders means handed to HTTP/2's writer, not acknowledged by the peer.
	return fmt.Sprintf(" [member=%d generation=%d phase=%s reused=%t inflight=%d sockets=%d last-rx=%s tcp=%s ready=%s headers=%s first=%s]", member.id, member.generation, phase, reused, member.inflight.Load(), sockets, lastRead, tcp, ready, headers, first)
}

func traceDuration(d time.Duration) string {
	if d < 0 {
		return "unknown"
	}
	return d.Round(time.Millisecond).String()
}
