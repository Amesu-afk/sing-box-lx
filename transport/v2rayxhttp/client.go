// Package v2rayxhttp implements the client side of the Xray "XHTTP"
// (a.k.a. "splithttp") v2ray transport for sing-box-lx. It is a lean-native
// implementation written on sing-box/sing primitives and the in-tree
// v2rayhttp HTTP/2 conn helpers, rather than vendoring Xray internals.
// See SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT.
//
// Wire protocol (mirrors Xray-core transport/internet/splithttp):
//
//	A random per-dial session id is generated. Requests target
//	"<path>/<sessionId>" (and, for upload packets, "<path>/<sessionId>/<seq>").
//	Every request carries a random-length X-Padding header in the
//	configured x_padding_bytes range to blur the on-wire size signature.
//
//	stream-one : a single POST whose request body carries client->server
//	             bytes and whose response body carries server->client bytes
//	             (one fully bidirectional HTTP/2 stream). Closest to
//	             httpupgrade; this is the mode "auto" falls back to here.
//	stream-up  : a single streamed POST for the upload direction plus a
//	             separate GET whose response body is the download direction.
//	packet-up  : a GET download stream plus sequential POST upload packets,
//	             each "<path>/<sessionId>/<seq>" carrying one write.
package v2rayxhttp

import (
	"context"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sHTTP "github.com/sagernet/sing/protocol/http"
	"github.com/sagernet/sing/service"

	"golang.org/x/net/http2"
)

const (
	modeAuto      = "auto"
	modePacketUp  = "packet-up"
	modeStreamUp  = "stream-up"
	modeStreamOne = "stream-one"
)

var _ adapter.V2RayClientTransport = (*Client)(nil)

// transportPoolSize is how many independent HTTP/2 transports (and therefore
// underlying TLS connections) a client spreads its streams over. Keep the
// pool bounded at eight: reducing it can concentrate long-lived video streams
// on too few TCP congestion controllers and worsen head-of-line blocking.
//
// Every proxied connection is one HTTP/2 stream, and a single transport puts
// them all on one TCP connection. That collapses under the long-lived streams a
// video session holds open: measured against a live Xray XHTTP inbound, a probe
// request took 0.4s with 10 held streams, 2.6s with 50, and timed out past 100.
// Spreading dials round-robin keeps each connection's stream count low.
const transportPoolSize = 8

// slotRefreshDebounce collapses a burst of handshake timeouts (every initial
// dial of a page hits the same stale pool at once, right after the radio wakes)
// into a single pool refresh.
const slotRefreshDebounce = 3 * time.Second

// freshHandshakeTimeout is the budget for a dial over a pool member that has
// never answered yet, and so must still pay TCP connect + TLS/REALITY + the
// HTTP/2 preface before the request even goes out.
//
// It is deliberately much larger than [handshakeTimeout]. That one is calibrated
// for a warm connection (~330ms measured, even with 100 streams on the pool),
// where anything past a few seconds means the socket is dead. Applying the same
// number to a cold dial gets it wrong in the expensive direction: four-ish round
// trips at a 1.5s RTT is already past 5s, so a slow-but-working link would fail
// instead of merely lagging — and on the commonest TarnVPN setup (XHTTP over
// REALITY, which resolves to stream-one) there is no retry to absorb it, because
// a stream-one dial carries its upload body on the same stream and cannot be
// replayed. Being generous here costs a longer wait in the genuinely-dead case,
// which the pool refresh then fixes for every subsequent dial.
// A var (not const) so tests can shrink it, mirroring handshakeTimeout.
var freshHandshakeTimeout = 15 * time.Second

// slowWarmThreshold is the old, tighter warm budget. A warm handshake that now
// succeeds but took longer than this would have been declared dead and churned the
// whole pool before handshakeTimeout was raised. It is logged (never acted on) so a
// device test can measure how often the old value was tripping under load, apart
// from the ones that still time out outright. A var so a test can move it.
var slowWarmThreshold = 5 * time.Second

// No per-connection health check (http2.Transport ReadIdleTimeout/PingTimeout)
// is configured here, on purpose. That probe is whole-ClientConn: when a PONG
// misses its window the transport tears the connection down and aborts EVERY
// multiplexed stream with "http2: client connection lost". Because the pool
// multiplexes many independent proxied connections onto each member, one probe
// timeout — trivially caused on mobile by a PONG queued behind another member
// saturating the shared radio — kills a whole batch of live, merely-idle
// streams (messenger push sockets, keep-alives) at once, which the user sees as
// apps freezing and reconnecting.
//
// Instead, staleness is reaped at DIAL granularity: a new dial bounds the time
// until its first response header (handshakeRoundTrip). A miss means the pooled
// connection is dead — the classic post-idle zombie, where the phone slept, NAT
// dropped the silent TCP connections, and nothing tore them down. On a miss the
// whole pool is swapped for fresh transports (refreshAllSlots). Crucially the
// swap only redirects NEW dials; goroutines already relaying over a member keep
// their own reference, so a false positive costs a few extra connections, never
// a healthy-stream teardown. That is the property the per-conn ping lacked.

// poolTransport is a pool member plus the one bit of state the dial budget needs:
// whether this transport has ever produced a response header.
//
// A member that has answered before owns a live TCP+TLS session, so the next dial
// over it is a single round trip and anything slower means the connection died.
// A member that has never answered still has to do everything: TCP connect, the
// TLS/REALITY handshake, the HTTP/2 preface, then the request — four-ish round
// trips, which on a bad mobile link is seconds, not milliseconds. Holding both to
// the same deadline is what would turn a slow-but-working link into a failing one
// (see [freshHandshakeTimeout]).
//
// It holds the member behind an http.RoundTripper rather than embedding
// *http2.Transport: the concrete type cannot be substituted, and the cold/warm
// budget is exactly the kind of timing behaviour that needs a stand-in to be
// testable at all.
type poolTransport struct {
	rt                http.RoundTripper
	warm              atomic.Bool
	connectionsMu     sync.Mutex
	activeConnections map[*trackedPoolConn]struct{}
}

func (t *poolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.rt.RoundTrip(req)
}

// CloseIdleConnections forwards to the underlying transport when it has one, so a
// pool member stays reapable through the same call the bare transport offered.
func (t *poolTransport) CloseIdleConnections() {
	if closer, ok := t.rt.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// trackConnection makes the TCP/TLS socket owned by this one pool member
// explicitly closeable. http2.Transport only exposes CloseIdleConnections;
// that is insufficient after a member has timed out while another long-lived
// stream (for example YouTube's reused media connection) still keeps the socket
// non-idle. Without tracking, the pool can route new dials elsewhere but cannot
// wake the app-side connection that remains pinned to the dead member.
func (t *poolTransport) trackConnection(conn net.Conn) net.Conn {
	tracked := &trackedPoolConn{Conn: conn, owner: t}
	t.connectionsMu.Lock()
	if t.activeConnections == nil {
		t.activeConnections = make(map[*trackedPoolConn]struct{})
	}
	t.activeConnections[tracked] = struct{}{}
	t.connectionsMu.Unlock()
	return tracked
}

func (t *poolTransport) forgetConnection(conn *trackedPoolConn) {
	t.connectionsMu.Lock()
	delete(t.activeConnections, conn)
	t.connectionsMu.Unlock()
}

// forceCloseConnections is reserved for a member that has already failed its
// handshake budget. Recent inbound traffic keeps a shared socket alive: a single
// request timeout is not proof that its sibling streams have failed.
func (t *poolTransport) forceCloseConnections() int {
	t.connectionsMu.Lock()
	connections := make([]*trackedPoolConn, 0, len(t.activeConnections))
	for conn := range t.activeConnections {
		connections = append(connections, conn)
	}
	t.connectionsMu.Unlock()
	closed := 0
	for _, conn := range connections {
		// A timed-out request does not prove the shared HTTP/2 connection is dead.
		// Preserve sockets that are still receiving bytes for sibling streams.
		if lastRead := conn.lastRead.Load(); lastRead != 0 && time.Since(time.Unix(0, lastRead)) < handshakeTimeout {
			continue
		}
		_ = conn.Close()
		closed++
	}
	return closed
}

type trackedPoolConn struct {
	net.Conn
	owner    *poolTransport
	once     sync.Once
	lastRead atomic.Int64
}

func (c *trackedPoolConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *trackedPoolConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.forgetConnection(c) })
	return err
}

// transportSlot holds one pool member behind an atomically swappable pointer, so
// a member whose connection has gone stale can be replaced without disturbing an
// in-flight dial that already captured the old transport.
//
// inflight counts the proxied connections currently leased to this slot — not the
// dials it has served. That distinction is the whole point: dials are short, the
// streams they open are not, and it is the live stream count that decides how much
// a member's single TCP connection has to carry (see [Client.acquireSlot]).
type transportSlot struct {
	ptr      atomic.Pointer[poolTransport]
	inflight atomic.Int64
}

func (s *transportSlot) get() *poolTransport { return s.ptr.Load() }

// slotLease is one dial's claim on a pool member: the transport every request of
// that dial must use, plus the release that hands the capacity back when the
// proxied connection ends. Every conn type owns its lease and releases it exactly
// once from Close.
type slotLease struct {
	slot *transportSlot
	rt   *poolTransport
	once sync.Once
}

func (l *slotLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() { l.slot.inflight.Add(-1) })
}

type Client struct {
	ctx        context.Context
	dialer     N.Dialer
	serverAddr M.Socksaddr
	// newTransport builds a fresh HTTP/2 transport (and therefore a fresh
	// underlying TLS/REALITY connection on first use). Held so a stale slot can
	// be swapped for a live one.
	newTransport    func() *poolTransport
	slots           []*transportSlot
	slotIdx         atomic.Uint64
	refreshMu       sync.Mutex
	lastRefresh     time.Time
	scheme          string
	host            string
	path            string
	mode            string
	headers         http.Header
	paddingRange    intRange
	sessionIDTable  string
	sessionIDLength intRange
	// meta holds the normalized placement/key/method selection (session, seq,
	// uplink-data, X-Padding obfs). Computed once in NewClient.
	meta metaConfig
	// realityEnabled records whether the TLS config is a Reality client config.
	// It drives mode=auto resolution (Reality → stream-one, like Xray).
	realityEnabled bool
	// noGRPCHeader suppresses the default "Content-Type: application/grpc" on
	// streamed-body requests (stream-one, stream-up). See option.NoGRPCHeader.
	noGRPCHeader bool
	// plog is the pool's diagnostic logger (nil in unit tests). The counters below
	// ride the connect path but are plain atomics touched only on the rare
	// pool-health events under investigation (stutter / speed drops), so they cost
	// nothing on the hot path. Temporary instrumentation for the on-device test.
	plog          log.ContextLogger
	warmTimeouts  atomic.Int64
	coldTimeouts  atomic.Int64
	slowWarm      atomic.Int64
	poolRefreshes atomic.Int64
}

// NewClient builds an XHTTP client transport. The tlsConfig (possibly Reality)
// is consumed exactly like the other v2ray transports: when present it drives
// an HTTP/2 dialer over the TLS dialer; when absent a plaintext HTTP/2 (h2c)
// transport is used.
func NewClient(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayXHTTPOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
	mode := options.Mode
	if mode == "" {
		mode = modeAuto
	}
	switch mode {
	case modeAuto, modePacketUp, modeStreamUp, modeStreamOne:
	default:
		return nil, E.New("v2ray-xhttp: unknown mode: ", mode)
	}

	paddingRange, err := parseRangeOr(options.XPaddingBytes, "x_padding_bytes", intRange{100, 1000})
	if err != nil {
		return nil, err
	}
	sessionIDTable, sessionIDLength, err := normalizeSessionIDOptions(options.SessionIDTable, options.SessionIDLength)
	if err != nil {
		return nil, err
	}

	meta, err := normalizeMeta(metaOptions{
		SessionPlacement:     options.SessionPlacement,
		SessionKey:           options.SessionKey,
		SeqPlacement:         options.SeqPlacement,
		SeqKey:               options.SeqKey,
		UplinkDataPlacement:  options.UplinkDataPlacement,
		UplinkDataKey:        options.UplinkDataKey,
		UplinkChunkSize:      options.UplinkChunkSize,
		UplinkHTTPMethod:     options.UplinkHTTPMethod,
		XPaddingObfsMode:     options.XPaddingObfsMode,
		XPaddingKey:          options.XPaddingKey,
		XPaddingHeader:       options.XPaddingHeader,
		XPaddingPlacement:    options.XPaddingPlacement,
		XPaddingMethod:       options.XPaddingMethod,
		ScMaxEachPostBytes:   options.ScMaxEachPostBytes,
		ScMinPostsIntervalMs: options.ScMinPostsIntervalMs,
	}, mode)
	if err != nil {
		return nil, err
	}

	// Build a pool of independent transports. They are identical in behaviour;
	// keeping them separate is what forces separate TLS connections, since a
	// single http2.Transport happily multiplexes every stream onto one. The
	// factory is retained so a stale member can be rebuilt in place.
	var (
		scheme       string
		newTransport func() *poolTransport
	)
	if tlsConfig == nil {
		scheme = "http"
		// Plaintext h2c: speak HTTP/2 over a cleartext TCP conn so the same
		// streaming request/response body machinery works without TLS.
		newTransport = func() *poolTransport {
			member := &poolTransport{}
			member.rt = &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
					conn, err := dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
					if err != nil {
						return nil, err
					}
					return member.trackConnection(conn), nil
				},
			}
			return member
		}
	} else {
		scheme = "https"
		if len(tlsConfig.NextProtos()) == 0 {
			tlsConfig.SetNextProtos([]string{http2.NextProtoTLS})
		}
		tlsDialer := tls.NewDialer(dialer, tlsConfig)
		newTransport = func() *poolTransport {
			member := &poolTransport{}
			member.rt = &http2.Transport{
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
					conn, err := tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
					if err != nil {
						return nil, err
					}
					return member.trackConnection(conn), nil
				},
			}
			return member
		}
	}
	slots := make([]*transportSlot, transportPoolSize)
	for i := range slots {
		s := &transportSlot{}
		// Cold by construction: none of these has dialed yet, so the first dial over
		// each gets freshHandshakeTimeout.
		s.ptr.Store(newTransport())
		slots[i] = s
	}

	var host string
	if options.Host != "" {
		host = options.Host
	} else if tlsConfig != nil && tlsConfig.ServerName() != "" {
		host = tlsConfig.ServerName()
	} else {
		host = serverAddr.String()
	}

	// Keep the configured path verbatim (only guarantee a leading slash). A
	// trailing slash is load-bearing: reverse proxies (e.g. nginx `location
	// /upload/ {}`) 301-redirect a bare "/upload" to "/upload/", and our download
	// RoundTrip does not follow redirects, so the 301 surfaces as a dial error.
	// The one place the slash must go is stream-one's bare path (empty sessionId),
	// where the Xray server keys the bidirectional branch on an exact bare path —
	// that trim happens locally in applyMeta, not globally here (lx: SPEC 002).
	path := options.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	headers := make(http.Header)
	for key, value := range options.Headers {
		headers[key] = value
	}

	// Best-effort: the v2ray transport registry hands us only a context, so pull the
	// log factory out of it. Absent (unit tests, a bare context) leaves plog nil and
	// every diagnostic call is guarded.
	var plog log.ContextLogger
	if factory := service.FromContext[log.Factory](ctx); factory != nil {
		plog = factory.NewLogger("xhttp-pool")
	}

	return &Client{
		ctx:             ctx,
		dialer:          dialer,
		serverAddr:      serverAddr,
		newTransport:    newTransport,
		slots:           slots,
		scheme:          scheme,
		host:            host,
		path:            path,
		mode:            mode,
		headers:         headers,
		paddingRange:    paddingRange,
		sessionIDTable:  sessionIDTable,
		sessionIDLength: sessionIDLength,
		meta:            meta,
		realityEnabled:  tlsConfigIsReality(tlsConfig),
		noGRPCHeader:    options.NoGRPCHeader,
		plog:            plog,
	}, nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	sessionID := newSessionID(c.sessionIDTable, c.sessionIDLength)
	switch c.mode {
	case modeAuto:
		// Match Xray's auto resolution (transport/internet/splithttp/dialer.go):
		// Reality → stream-one; otherwise → packet-up (the most broadly compatible
		// mode, live-validated against Xray 3x-ui). Xray also picks stream-up when
		// downloadSettings is present, but we don't support asymmetric transport.
		if c.realityEnabled {
			return c.dialStreamOne(ctx, sessionID)
		}
		return c.dialPacketUp(ctx, sessionID)
	case modePacketUp:
		return c.dialPacketUp(ctx, sessionID)
	case modeStreamUp:
		return c.dialStreamUp(ctx, sessionID)
	case modeStreamOne:
		return c.dialStreamOne(ctx, sessionID)
	default:
		return nil, E.New("v2ray-xhttp: unknown mode: ", c.mode)
	}
}

func (c *Client) Close() error {
	for _, s := range c.slots {
		if tr := s.get(); tr != nil {
			tr.CloseIdleConnections()
		}
	}
	return nil
}

// acquireSlot leases the least-loaded pool member to one dial, counting the lease
// against that member until the proxied connection closes.
//
// Round-robin — what this replaces — balances *dials*, which is not the quantity
// that hurts. A dial is over in one round trip; the stream it opens can live for
// the whole session, and it is streams that a member's single TCP connection has
// to carry (a probe took 0.4s with 10 held streams, 2.6s with 50). A short-video
// feed opens and drops connections constantly with a handful outliving the rest,
// so an even split of dials drifts into a very uneven split of live streams: the
// member that happened to collect the long ones keeps being handed more, and its
// connection is where every new dial then queues, stalls behind TCP head-of-line
// blocking, and eventually trips the handshake budget.
//
// The scan is over the small pool and stops at the first empty one, so it costs less
// than the atomic increment it replaces. Ties break at a rotating offset, which
// keeps the all-equal case (an idle pool) exactly round-robin.
//
// All requests belonging to one dial must share the member they were opened with:
// the upload and download halves of stream-up/packet-up are paired by session id,
// and keeping them on a single connection preserves their ordering. That is why
// the lease captures the *transport*, not the slot — a concurrent refresh must
// never split a dial's halves across two connections.
func (c *Client) acquireSlot() *slotLease {
	start := c.slotIdx.Add(1) - 1
	size := uint64(len(c.slots))
	best := c.slots[start%size]
	bestLoad := best.inflight.Load()
	for i := uint64(1); i < size && bestLoad > 0; i++ {
		candidate := c.slots[(start+i)%size]
		if load := candidate.inflight.Load(); load < bestLoad {
			best, bestLoad = candidate, load
		}
	}
	best.inflight.Add(1)
	return &slotLease{slot: best, rt: best.get()}
}

// refreshAllSlots swaps every pool member for a fresh transport, dropping the
// stale connections. It is triggered when a dial's handshake times out — the
// tell-tale of a pool gone stale after the radio slept and NAT dropped the idle
// TCP connections (see the package comment). Swapping the pointer does NOT tear
// down a member's in-flight streams: goroutines already relaying over it keep
// their own reference; only new dials get the fresh connection. So a false
// positive (a merely-slow handshake) costs at most a few extra connections,
// never a healthy-stream teardown. Debounced so the storm of stalled initial
// dials right after wake refreshes the pool exactly once.
func (c *Client) refreshAllSlots() {
	now := timeNow()
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if !c.lastRefresh.IsZero() && now.Sub(c.lastRefresh) < slotRefreshDebounce {
		return
	}
	c.lastRefresh = now
	if n := c.poolRefreshes.Add(1); c.plog != nil {
		c.plog.Warn("xhttp-pool: swapping all ", len(c.slots),
			" transports after a handshake timeout (pool-refreshes=", n,
			") — a burst then quiet is a post-idle wake; a steady drip under load is churn")
	}
	for _, s := range c.slots {
		old := s.ptr.Swap(c.newPoolTransport())
		if old != nil {
			old.CloseIdleConnections()
		}
	}
}

// newPoolTransport wraps a freshly built transport as a cold pool member, so its
// first dial is measured against freshHandshakeTimeout rather than the warm one.
func (c *Client) newPoolTransport() *poolTransport {
	return c.newTransport()
}

// handshakeVia runs one handshake-bounded round trip over a pool member, giving a
// never-yet-used transport the cold budget, and promoting it to the warm one the
// moment it proves it can answer.
func (c *Client) handshakeVia(pt *poolTransport, req *http.Request) (*http.Response, error) {
	warm := pt.warm.Load()
	timeout := freshHandshakeTimeout
	if warm {
		timeout = handshakeTimeout
	}
	start := timeNow()
	response, err := handshakeRoundTrip(pt, req, timeout)
	elapsed := timeNow().Sub(start)
	if err == nil {
		pt.warm.Store(true)
		// A warm dial that answered only just inside the (now larger) budget is the
		// tell-tale of the churn under investigation: on the old 5s budget it would
		// have been declared dead and refreshed the whole pool. Logged, not acted on.
		if warm && elapsed >= slowWarmThreshold {
			n := c.slowWarm.Add(1)
			if c.plog != nil {
				c.plog.Warn("xhttp-pool: slow warm handshake ", elapsed.Round(time.Millisecond),
					" within budget ", timeout, " — would have tripped the old 5s budget (slow-warm=", n, ")")
			}
		}
		return response, nil
	}
	if err == errHandshakeTimeout {
		if warm {
			n := c.warmTimeouts.Add(1)
			if c.plog != nil {
				c.plog.Warn("xhttp-pool: WARM member handshake timed out after ", timeout,
					" — pool refresh incoming (warm-timeouts=", n,
					"); inspect recent traffic to distinguish congestion from a dead link")
			}
		} else {
			n := c.coldTimeouts.Add(1)
			if c.plog != nil {
				c.plog.Warn("xhttp-pool: cold member handshake timed out after ", timeout,
					" (cold-timeouts=", n, ")")
			}
		}
	}
	return response, err
}

// retireTransport is the whole reaction to a handshake timeout observed on rt:
// refresh the pool, then close rt's underlying sockets.
//
// The second half is not redundant. refreshAllSlots can only close what it swaps,
// and at the instant it runs rt still owns the stream that is in the middle of
// timing out — so the connection is not idle and CloseIdleConnections skips it.
// Moments later the cancelled stream is removed and rt becomes idle, but by then
// rt is unreachable from the slots, so no future refresh will ever revisit it:
// the swap only ever closes the pointer it just replaced. The zombie TCP
// connection and its http2 readLoop goroutine would then stay for as long as the
// peer keeps them (on a NAT-dropped link: forever). The debounce widens the same
// hole — a sibling dial timing out inside the window skips the refresh entirely.
//
// Calling it after refreshAllSlots is what makes the ordering work: by then rt is
// out of rotation. A production poolTransport reaps silent sockets but preserves
// sockets with recent inbound traffic, where congestion may explain the timeout.
// Other members' live streams are also preserved. The generic fallback keeps
// tests and alternate round-trippers reapable through CloseIdleConnections.
func (c *Client) retireTransport(rt http.RoundTripper) {
	c.refreshAllSlots()
	if member, ok := rt.(*poolTransport); ok {
		closed := member.forceCloseConnections()
		if closed > 0 && c.plog != nil {
			c.plog.Warn("xhttp-pool: force-closed ", closed,
				" active socket(s) on the timed-out member so pinned app connections can reopen")
		}
		member.CloseIdleConnections()
		return
	}
	// Matched by capability, not by concrete type: *http2.Transport and *http.Transport
	// both qualify, and a test can hand in a recorder.
	if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// baseURL builds a fresh request URL targeting the normalized base path. The
// placement engine (applyMeta) appends session/seq path segments and query params
// as configured; applyXPadding attaches the padding. The base path is set via
// sHTTP.URLSetPath so percent-encoding matches the rest of sing-box.
func (c *Client) baseURL() (*url.URL, error) {
	u := &url.URL{
		Scheme: c.scheme,
		Host:   c.serverAddr.String(),
	}
	if err := sHTTP.URLSetPath(u, c.path); err != nil {
		return nil, E.Cause(err, "parse path")
	}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u, nil
}

// newRequest constructs an XHTTP request: it builds the base URL, lets the
// placement engine position the sessionID and (packet-up) seqStr, then attaches
// X-Padding. An empty sessionID emits no session metadata (stream-one targets the
// bare path with no sessionId, which is how the server routes the bidirectional
// branch). An empty seqStr emits no seq (stream modes).
func (c *Client) newRequest(ctx context.Context, method, sessionID, seqStr string, body interface{ Read([]byte) (int, error) }) (*http.Request, error) {
	u, err := c.baseURL()
	if err != nil {
		return nil, err
	}
	basePath := u.Path
	request := &http.Request{
		Method: method,
		URL:    u,
		Header: c.headers.Clone(),
		Host:   c.host,
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	c.applyMeta(request, basePath, sessionID, seqStr)
	c.applyXPadding(request)
	if body != nil {
		request.Body = readCloser{body}
	}
	return request.WithContext(ctx), nil
}

// Xray's current XHTTP client can generate a session id from a configured ASCII
// table and length range. With no table it preserves the historical dashed UUID
// (8-4-4-4-12). The server treats either form as an opaque grouping key.
var predefinedSessionIDTables = map[string]string{
	"ALPHABET": "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Alphabet": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"BASE36":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Base62":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"HEX":      "0123456789ABCDEF",
	"alphabet": "abcdefghijklmnopqrstuvwxyz",
	"base36":   "0123456789abcdefghijklmnopqrstuvwxyz",
	"hex":      "0123456789abcdef",
	"number":   "0123456789",
}

func normalizeSessionIDOptions(table, length string) (string, intRange, error) {
	if predefined, exists := predefinedSessionIDTables[table]; exists {
		table = predefined
	}
	if table == "" {
		return "", intRange{}, nil
	}
	for index := range len(table) {
		if table[index] >= 0x80 {
			return "", intRange{}, E.New("v2ray-xhttp: session_id_table must contain only ASCII characters")
		}
	}
	idLength, err := parseRangeOr(length, "session_id_length", intRange{})
	if err != nil {
		return "", intRange{}, err
	}
	if idLength.min <= 0 {
		return "", intRange{}, E.New("v2ray-xhttp: session_id_length must be greater than zero when session_id_table is set")
	}
	room := new(big.Int)
	base := big.NewInt(int64(len(table)))
	for size := idLength.min; size <= idLength.max; size++ {
		room.Add(room, new(big.Int).Exp(base, big.NewInt(int64(size)), nil))
	}
	if room.Cmp(big.NewInt(2<<30)) < 0 {
		return "", intRange{}, E.New("v2ray-xhttp: session_id_table or session_id_length is too small")
	}
	return table, idLength, nil
}

func newSessionID(table string, length intRange) string {
	if table != "" && length.min > 0 {
		id := make([]byte, length.rand())
		for index := range id {
			id[index] = table[rand.Intn(len(table))]
		}
		return string(id)
	}
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	const hexdigits = "0123456789abcdef"
	var h [32]byte
	for i, v := range b {
		h[i*2] = hexdigits[v>>4]
		h[i*2+1] = hexdigits[v&0x0f]
	}
	return string(h[0:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:32])
}

// readCloser adapts a plain reader to io.ReadCloser for use as a request body
// without pulling in an extra import.
type readCloser struct {
	r interface{ Read([]byte) (int, error) }
}

func (r readCloser) Read(p []byte) (int, error) { return r.r.Read(p) }
func (r readCloser) Close() error               { return nil }

// drainAndClose fully discards then closes an HTTP response body.
func drainAndClose(body interface {
	Read([]byte) (int, error)
	Close() error
},
) {
	buffer := buf.Get(buf.BufferSize)
	for {
		if _, err := body.Read(buffer); err != nil {
			break
		}
	}
	buf.Put(buffer)
	_ = body.Close()
}
