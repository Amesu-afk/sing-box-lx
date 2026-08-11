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

// applyGRPCHeader sets the streamed-body Content-Type that Xray sends on
// stream-one/stream-up requests (FillStreamRequest in
// transport/internet/splithttp/config.go). Reverse proxies and CDNs in front of
// an XHTTP server key response streaming on a gRPC content type; without it the
// download side is buffered and the dial hangs until timeout. Opt out with
// no_grpc_header, matching Xray's NoGRPCHeader.
func (c *Client) applyGRPCHeader(request *http.Request) {
	if c.noGRPCHeader || request.Body == nil {
		return
	}
	request.Header.Set("Content-Type", "application/grpc")
}

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
	c.applyGRPCHeader(request)

	rt := c.pickSlot().get()
	conn := newStreamConn(pipeReader, pipeWriter, c.serverAddr)
	// lx: 050 — the conn is handed up before RoundTrip has raised the stream, so
	// anything written meanwhile (the VLESS/encryption handshake) blocks on an
	// unread pipe. Until the stream is up, cancelling the dial context must free
	// that write; the guard stops at `created` so it can never tear down a live
	// connection once the stream exists.
	stopGuard := watchDialContext(ctx, conn.created, func(err error) {
		// Break the pipe from the read half so the blocked Write sees this error
		// rather than a bare ErrClosedPipe (see writeDeadline).
		pipeReader.CloseWithError(err)
	})
	go func() {
		defer stopGuard()
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

// watchDialContext releases a pending write on the upload pipe if the dial
// context is cancelled before the stream is up. It returns a stop function; the
// watcher also exits on `done`, so a connection that reached the live stage is
// never affected by later cancellation of its dial context (SPECS/TASKS/050).
func watchDialContext(ctx context.Context, done <-chan struct{}, onCancel func(error)) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			onCancel(ctx.Err())
		case <-done:
		case <-stop:
		}
	}()
	return func() { close(stop) }
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
	c.applyGRPCHeader(upReq)
	conn := newSplitConn(downResp.Body, pipeReader, pipeWriter, c.serverAddr)
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

// lx:begin 050 deadline-support (SPECS/TASKS/050)
//
// A streamed body is an io.Pipe: a Write blocks until RoundTrip starts reading
// it, which on a half-alive node (TCP accepted, stream never raised) is never.
// io.Pipe has no deadlines of its own, but CloseWithError releases a pending
// Write instantly — so a deadline is a timer that closes the pipe with
// os.ErrDeadlineExceeded. Without this the blocked goroutine is unkillable and
// outlives box shutdown; see the task for the field evidence.
//
// The read side is a late-bound response body, so it cannot be closed before it
// exists. Read therefore waits on `dead` alongside `created`, and an expired
// read deadline closes `dead` rather than touching the reader.
// The pipe is broken from the READ half on purpose: io.Pipe hands a writer only
// ErrClosedPipe for an error set via PipeWriter.CloseWithError (writeCloseError
// prefers rerr, and werr suppresses it), so closing the write half would lose
// os.ErrDeadlineExceeded. Closing the read half sets rerr, which the blocked
// Write does surface.
type writeDeadline struct {
	access sync.Mutex
	reader *io.PipeReader
	timer  *time.Timer
}

// set arms (or re-arms) the write deadline. A zero time clears it; a time in the
// past fires immediately, matching net.Conn semantics.
func (d *writeDeadline) set(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() || d.reader == nil {
		return nil
	}
	if delay := time.Until(t); delay <= 0 {
		d.reader.CloseWithError(os.ErrDeadlineExceeded)
	} else {
		d.timer = time.AfterFunc(delay, func() {
			d.reader.CloseWithError(os.ErrDeadlineExceeded)
		})
	}
	return nil
}

// stop releases the timer; called from Close so an armed deadline cannot outlive
// the conn.
func (d *writeDeadline) stop() {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// readDeadline unblocks a pending Read by closing `dead`. Read observes it both
// while waiting for the late-bound reader and while blocked in reader.Read —
// the latter needs the reader closed too, which the owner does via onExpire.
type readDeadline struct {
	access   sync.Mutex
	dead     chan struct{}
	expired  bool
	timer    *time.Timer
	onExpire func()
}

func newReadDeadline(onExpire func()) *readDeadline {
	return &readDeadline{dead: make(chan struct{}), onExpire: onExpire}
}

func (d *readDeadline) set(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() || d.expired {
		return nil
	}
	if delay := time.Until(t); delay <= 0 {
		d.expireLocked()
	} else {
		d.timer = time.AfterFunc(delay, d.expire)
	}
	return nil
}

func (d *readDeadline) expire() {
	d.access.Lock()
	defer d.access.Unlock()
	d.expireLocked()
}

func (d *readDeadline) expireLocked() {
	if d.expired {
		return
	}
	d.expired = true
	close(d.dead)
	if d.onExpire != nil {
		d.onExpire()
	}
}

func (d *readDeadline) stop() {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// lx:end 050 deadline-support

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
	// lx: 050 — deadlines; without them a blocked Write/Read is unkillable.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
	// closed records that Close() ran. It lets a RoundTrip that lands afterwards
	// dispose of its response body itself — see Close for why that matters.
	closed atomic.Bool
}

func newStreamConn(reader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr) *streamConn {
	conn := &streamConn{
		writer:     writer,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
	}
	conn.writeDeadline.reader = reader
	// lx: 050 — an expired read deadline must also close an already-bound reader,
	// otherwise Read stays blocked inside reader.Read.
	conn.readDeadline = newReadDeadline(func() {
		select {
		case <-conn.created:
			if conn.reader != nil {
				conn.reader.Close()
			}
		default:
		}
	})
	return conn
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
	//
	// lx: 050 — also wait on the read deadline: until RoundTrip binds the reader
	// there is nothing to close, so an expired deadline can only be observed here.
	select {
	case <-c.created:
	case <-c.readDeadline.dead:
		return 0, os.ErrDeadlineExceeded
	}
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
		// lx: 050 — drop armed timers first so neither can fire on a closed conn.
		c.writeDeadline.stop()
		c.readDeadline.stop()
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

func (c *streamConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *streamConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — real deadlines (were os.ErrInvalid): a Write into an unread upload
// pipe is otherwise unkillable and survives box shutdown.
func (c *streamConn) SetDeadline(t time.Time) error {
	if err := c.readDeadline.set(t); err != nil {
		return err
	}
	return c.writeDeadline.set(t)
}

func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.writeDeadline.set(t) }

// NeedAdditionalReadDeadline stays true even though SetReadDeadline now works:
// the read deadline here is one-shot (it closes the late-bound body to break a
// pending Read) and does not restore the conn for a later read, which is what
// net.Conn semantics and deadline.NewConn provide. The escape this task needs is
// on the write side, so keep the wrapper rather than claim semantics we lack.
func (c *streamConn) NeedAdditionalReadDeadline() bool { return true }

// splitConn pairs an already-open download reader with an upload pipe (stream-up
// mode). The download body is ready immediately; the upload POST is driven by
// the caller in a goroutine.
type splitConn struct {
	reader     io.ReadCloser
	writer     *io.PipeWriter
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// lx: 050 — same unkillable-Write exposure as streamConn; the reader here is
	// bound up front, so an expired read deadline just closes it.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
}

func newSplitConn(reader io.ReadCloser, uploadReader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr) *splitConn {
	conn := &splitConn{
		reader:     reader,
		writer:     writer,
		serverAddr: serverAddr,
	}
	conn.writeDeadline.reader = uploadReader
	conn.readDeadline = newReadDeadline(func() {
		if conn.reader != nil {
			conn.reader.Close()
		}
	})
	return conn
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
		// lx: 050 — drop armed timers before closing, as in streamConn.
		c.writeDeadline.stop()
		c.readDeadline.stop()
		c.writer.Close()
		c.reader.Close()
	})
	return nil
}

func (c *splitConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *splitConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — real deadlines (were os.ErrInvalid); see streamConn.
func (c *splitConn) SetDeadline(t time.Time) error {
	if err := c.readDeadline.set(t); err != nil {
		return err
	}
	return c.writeDeadline.set(t)
}

func (c *splitConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *splitConn) SetWriteDeadline(t time.Time) error { return c.writeDeadline.set(t) }

// NeedAdditionalReadDeadline: see streamConn — the read deadline is one-shot.
func (c *splitConn) NeedAdditionalReadDeadline() bool { return true }

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
