package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// handshakeTimeout bounds how long a dial over an already-warm pool member waits
// for its first response header before treating the connection as dead. A healthy
// XHTTP inbound answers a new stream immediately (measured ~330ms even with 100
// streams held on the pool), so 5s is a wide margin over a single round trip
// while still capping the post-idle zombie stall that otherwise runs to tens of
// seconds. A cold member is measured against [freshHandshakeTimeout] instead — it
// has a TLS/REALITY handshake to pay for first. A var (not const) so tests can
// shrink it, mirroring timeNow.
var handshakeTimeout = 5 * time.Second

// errHandshakeTimeout signals that a pooled connection produced no response
// header in time — the caller refreshes the pool and, for an idempotent GET
// dial, retries once on a fresh connection.
var errHandshakeTimeout = E.New("v2ray-xhttp: handshake timed out (stale pooled connection)")

// handshakeRoundTrip runs rt.RoundTrip(req) but bounds only the time until the
// response headers arrive; once they do, the stream may live indefinitely. On a
// timeout it cancels just this request — an RST_STREAM on this one stream, never
// a whole-connection teardown, so sibling streams multiplexed on the same
// connection are untouched — and returns errHandshakeTimeout. On success the
// response body takes ownership of the cancel func (fired on Close), so the
// bounded context is released without a leak and without cutting the stream.
func handshakeRoundTrip(rt http.RoundTripper, req *http.Request, timeout time.Duration) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(req.Context())
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		cancel()
	})
	resp, err := rt.RoundTrip(req.WithContext(reqCtx))
	if err != nil {
		timer.Stop()
		cancel()
		if timedOut.Load() {
			return nil, errHandshakeTimeout
		}
		return nil, err
	}
	if !timer.Stop() {
		// The timer fired in the narrow window between headers arriving and this
		// Stop, so the context is already cancelled and the stream is unusable.
		resp.Body.Close()
		cancel()
		return nil, errHandshakeTimeout
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody defers a context cancel to body Close, so the handshake-timeout
// context outlives the header phase and is released exactly when the stream ends.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

// dialDownloadStream opens the download side (a GET whose response body is the
// downlink) with a stale-connection guard. If the pooled member is a post-idle
// zombie its handshake times out; the pool is refreshed and the GET — idempotent
// and bodyless, so safe to replay — is retried once on a fresh connection. It
// returns the transport the download landed on so the matching uploads ride the
// same connection.
func (c *Client) dialDownloadStream(ctx context.Context, sessionID string) (http.RoundTripper, *http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := c.newRequest(ctx, http.MethodGet, sessionID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		rt := c.pickSlot().get()
		resp, err := c.handshakeVia(rt, req)
		if err == errHandshakeTimeout {
			c.retireTransport(rt)
			continue
		}
		if err != nil {
			return nil, nil, E.Cause(err, "open download")
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, nil, E.New("v2ray-xhttp: unexpected download status: ", resp.Status)
		}
		return rt, resp, nil
	}
	return nil, nil, E.Cause(errHandshakeTimeout, "open download")
}

// dialStreamOne opens a single bidirectional HTTP/2 stream: the request body is
// the upload direction, the response body is the download direction. With Reality
// it is also what "auto" resolves to (matching Xray).
//
// Unlike stream-up/packet-up, the request targets the BARE path with NO sessionId:
// Xray's splithttp server keys the stream-one (bidirectional) branch on an empty
// sessionId. Sending "<path>/<sessionId>" instead routes the server into the
// stream-down branch, which never pairs with a stream-up POST, so the response
// body carries non-VLESS bytes and the VLESS layer fails with "unknown version".
func (c *Client) dialStreamOne(ctx context.Context, sessionID string) (net.Conn, error) {
	_ = sessionID // intentionally unused: stream-one sends no sessionId on the wire
	pipeReader, pipeWriter := io.Pipe()
	// stream-one carries a body, so it uses the configured upload method (default
	// POST); the empty sessionID keeps the request on the bare path.
	request, err := c.newRequest(ctx, c.meta.uplinkHTTPMethod, "", "", pipeReader)
	if err != nil {
		return nil, err
	}

	rt := c.pickSlot().get()
	conn := newStreamConn(pipeWriter, c.serverAddr)
	go func() {
		response, err := c.handshakeVia(rt, request)
		if err != nil {
			// stream-one carries its upload body on the same stream, so it can't
			// be replayed on a fresh connection here; refresh the pool so the
			// app's immediate reopen lands on a live member, and fail this dial.
			if err == errHandshakeTimeout {
				c.retireTransport(rt)
			}
			conn.setupReader(nil, err)
			return
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			conn.setupReader(nil, E.New("v2ray-xhttp: unexpected status: ", response.Status))
			return
		}
		conn.setupReader(response.Body, nil)
	}()
	return conn, nil
}

// dialStreamUp opens a streamed POST for the upload direction and a separate GET
// whose response body is the download direction.
func (c *Client) dialStreamUp(ctx context.Context, sessionID string) (net.Conn, error) {
	// Download: GET response body (no seq — stream mode), stale-conn guarded.
	rt, downResp, err := c.dialDownloadStream(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// Upload: streamed body request using the configured upload method.
	pipeReader, pipeWriter := io.Pipe()
	upReq, err := c.newRequest(ctx, c.meta.uplinkHTTPMethod, sessionID, "", pipeReader)
	if err != nil {
		downResp.Body.Close()
		return nil, err
	}
	conn := newSplitConn(downResp.Body, pipeWriter, c.serverAddr)
	go func() {
		upResp, err := rt.RoundTrip(upReq)
		if err != nil {
			conn.uploadFailed(err)
			return
		}
		drainAndClose(upResp.Body)
	}()
	return conn, nil
}

// dialPacketUp opens a GET download stream and sends uploads as sequential POST
// packets, one HTTP request per Write.
func (c *Client) dialPacketUp(ctx context.Context, sessionID string) (net.Conn, error) {
	// Download stream: GET with the session id but no seq (downlink), guarded.
	rt, downResp, err := c.dialDownloadStream(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &packetConn{
		ctx:        ctx,
		client:     c,
		transport:  rt,
		sessionID:  sessionID,
		reader:     downResp.Body,
		serverAddr: c.serverAddr,
	}, nil
}

// streamConn is a net.Conn whose write side is the upload pipe and whose read
// side is a late-bound response body (download). It mirrors the late-binding
// pattern of v2rayhttp.HTTP2Conn but is self-contained here.
type streamConn struct {
	writer     *io.PipeWriter
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// closed records that Close() ran. It lets a RoundTrip that lands afterwards
	// dispose of its response body itself — see Close for why that matters.
	closed atomic.Bool
}

func newStreamConn(writer *io.PipeWriter, serverAddr M.Socksaddr) *streamConn {
	return &streamConn{
		writer:     writer,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
	}
}

func (c *streamConn) setupReader(reader io.ReadCloser, err error) {
	c.reader = reader
	c.readerErr = err
	close(c.created)
	// Close() may have already run and skipped the body because RoundTrip had
	// not produced one yet; in that case it is ours to release.
	if reader != nil && c.closed.Load() {
		reader.Close()
	}
}

func (c *streamConn) Read(b []byte) (int, error) {
	// Always synchronise on created before touching reader/readerErr: the RoundTrip
	// goroutine writes them before close(created), so the receive is the happens-
	// before edge. The old `if c.reader == nil` fast path read reader unsynchronised
	// (a data race, -race flagged it) for no gain — a receive on an already-closed
	// channel is effectively free (SPEC 022 #7).
	<-c.created
	if c.readerErr != nil {
		return 0, c.readerErr
	}
	n, err := c.reader.Read(b)
	return n, normalizeReadErr(err)
}

// normalizeReadErr folds x/net/http2's connection- and body-close sentinels into
// io.EOF. They are unexported errors.New values that wrap nothing, so the relay's
// E.IsClosedOrCanceled doesn't recognise them and route/conn.go logs every
// ordinary downlink close at ERROR. Matching by message (there is no exported
// sentinel to errors.Is against) and returning io.EOF makes the relay treat them
// as the normal stream end they are — a closed body or a lost pooled connection
// just means this proxied conn is done; the app reopens on its own.
func normalizeReadErr(err error) error {
	if err == nil {
		return nil
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "response body closed"),
		strings.Contains(msg, "client connection lost"),
		strings.Contains(msg, "client conn is closed"):
		return io.EOF
	default:
		return err
	}
}

func (c *streamConn) Write(b []byte) (int, error) {
	return c.writer.Write(b)
}

func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		// Publish the intent before probing created, so a RoundTrip finishing
		// concurrently is guaranteed to observe it and close the body itself.
		// Without this handoff the default branch below silently dropped bodies
		// belonging to still-in-flight requests, leaking one HTTP/2 stream each
		// time — they accumulate on the pooled connection until it hits the
		// peer's max-concurrent-streams and every new dial stalls.
		c.closed.Store(true)
		c.writer.Close()
		select {
		case <-c.created:
			if c.reader != nil {
				c.reader.Close()
			}
		default:
		}
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *streamConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *streamConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *streamConn) NeedAdditionalReadDeadline() bool   { return true }

// splitConn pairs an already-open download reader with an upload pipe (stream-up
// mode). The download body is ready immediately; the upload POST is driven by
// the caller in a goroutine.
type splitConn struct {
	reader     io.ReadCloser
	writer     *io.PipeWriter
	serverAddr M.Socksaddr
	closeOnce  sync.Once
}

func newSplitConn(reader io.ReadCloser, writer *io.PipeWriter, serverAddr M.Socksaddr) *splitConn {
	return &splitConn{
		reader:     reader,
		writer:     writer,
		serverAddr: serverAddr,
	}
}

func (c *splitConn) uploadFailed(err error) {
	c.writer.CloseWithError(err)
}

func (c *splitConn) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	return n, normalizeReadErr(err)
}
func (c *splitConn) Write(b []byte) (int, error) { return c.writer.Write(b) }

func (c *splitConn) Close() error {
	c.closeOnce.Do(func() {
		c.writer.Close()
		c.reader.Close()
	})
	return nil
}

func (c *splitConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *splitConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *splitConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *splitConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *splitConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *splitConn) NeedAdditionalReadDeadline() bool   { return true }

// packetConn implements packet-up: download is a GET response body, each Write
// is delivered as a sequential POST to "<path>/<sessionId>/<seq>".
type packetConn struct {
	ctx    context.Context
	client *Client
	// transport is the pool member this session was opened on; upload packets
	// must ride the same connection as the download stream they belong to.
	transport  http.RoundTripper
	sessionID  string
	reader     io.ReadCloser
	serverAddr M.Socksaddr
	access     sync.Mutex
	seq        uint64
	lastPost   time.Time
	closed     bool
}

func (c *packetConn) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	return n, normalizeReadErr(err)
}

// Write delivers a write as one or more sequential upload POSTs. A write larger
// than sc_max_each_post_bytes is split into multiple sequenced packets; successive
// posts are throttled by sc_min_posts_interval_ms (anti-burst). Each packet carries
// the payload per uplink_data_placement.
func (c *packetConn) Write(b []byte) (int, error) {
	maxEach := c.client.meta.scMaxEachPostBytes.rand()
	if maxEach <= 0 {
		maxEach = len(b)
	}
	written := 0
	for written < len(b) {
		end := written + maxEach
		if end > len(b) {
			end = len(b)
		}
		if err := c.sendPacket(b[written:end]); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

// sendPacket posts a single sequenced upload chunk.
func (c *packetConn) sendPacket(b []byte) error {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return net.ErrClosed
	}
	seq := c.seq
	c.seq++
	// Throttle: enforce the minimum inter-post interval since the last post.
	wait := c.nextPostDelay()
	c.access.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return c.ctx.Err()
		case <-timer.C:
		}
	}

	payload := make([]byte, len(b))
	copy(payload, b)
	request, err := c.client.newRequest(c.ctx, c.client.meta.uplinkHTTPMethod, c.sessionID, strconv.FormatUint(seq, 10), nil)
	if err != nil {
		return err
	}
	c.client.applyUplinkData(request, payload)

	response, err := c.transport.RoundTrip(request)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return E.New("v2ray-xhttp: unexpected upload status: ", response.Status)
	}
	drainAndClose(response.Body)
	return nil
}

// nextPostDelay returns how long to wait before the next post to honor
// sc_min_posts_interval_ms, updating lastPost to the projected post time. Caller
// must hold c.access.
func (c *packetConn) nextPostDelay() time.Duration {
	interval := time.Duration(c.client.meta.scMinPostsIntervalMs.rand()) * time.Millisecond
	now := timeNow()
	if c.lastPost.IsZero() || interval <= 0 {
		c.lastPost = now
		return 0
	}
	earliest := c.lastPost.Add(interval)
	if earliest.After(now) {
		c.lastPost = earliest
		return earliest.Sub(now)
	}
	c.lastPost = now
	return 0
}

func (c *packetConn) Close() error {
	c.access.Lock()
	c.closed = true
	c.access.Unlock()
	return c.reader.Close()
}

func (c *packetConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *packetConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *packetConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *packetConn) NeedAdditionalReadDeadline() bool   { return true }

// byteReader is a one-shot reader over a byte slice used as a fixed-length
// request body for packet-up uploads.
type byteReader struct {
	data []byte
	off  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
