// Package v2rayxhttp implements the client side of the Xray "XHTTP"
// (a.k.a. "splithttp") v2ray transport for sing-box-lx. It is a lean-native
// implementation written on sing-box/sing primitives and the in-tree
// v2rayhttp HTTP/2 conn helpers, rather than vendoring Xray internals.
// See SPECS/002-XHTTP_CLIENT_TRANSPORT.
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
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sHTTP "github.com/sagernet/sing/protocol/http"

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
// underlying TLS connections) a client spreads its streams over.
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

// transportSlot holds one pool member behind an atomically swappable pointer, so
// a member whose connection has gone stale can be replaced without disturbing an
// in-flight dial that already captured the old transport.
type transportSlot struct {
	ptr atomic.Pointer[http2.Transport]
}

func (s *transportSlot) get() *http2.Transport { return s.ptr.Load() }

type Client struct {
	ctx        context.Context
	dialer     N.Dialer
	serverAddr M.Socksaddr
	// newTransport builds a fresh HTTP/2 transport (and therefore a fresh
	// underlying TLS/REALITY connection on first use). Held so a stale slot can
	// be swapped for a live one.
	newTransport func() *http2.Transport
	slots        []*transportSlot
	slotIdx      atomic.Uint64
	refreshMu    sync.Mutex
	lastRefresh  time.Time
	scheme       string
	host         string
	path         string
	mode         string
	headers      http.Header
	paddingRange intRange
	// meta holds the normalized placement/key/method selection (session, seq,
	// uplink-data, X-Padding obfs). Computed once in NewClient.
	meta metaConfig
	// realityEnabled records whether the TLS config is a Reality client config.
	// It drives mode=auto resolution (Reality → stream-one, like Xray).
	realityEnabled bool
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
		newTransport func() *http2.Transport
	)
	if tlsConfig == nil {
		scheme = "http"
		// Plaintext h2c: speak HTTP/2 over a cleartext TCP conn so the same
		// streaming request/response body machinery works without TLS.
		newTransport = func() *http2.Transport {
			return &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
					return dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
				},
			}
		}
	} else {
		scheme = "https"
		if len(tlsConfig.NextProtos()) == 0 {
			tlsConfig.SetNextProtos([]string{http2.NextProtoTLS})
		}
		tlsDialer := tls.NewDialer(dialer, tlsConfig)
		newTransport = func() *http2.Transport {
			return &http2.Transport{
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
					return tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
				},
			}
		}
	}
	slots := make([]*transportSlot, transportPoolSize)
	for i := range slots {
		s := &transportSlot{}
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

	return &Client{
		ctx:            ctx,
		dialer:         dialer,
		serverAddr:     serverAddr,
		newTransport:   newTransport,
		slots:          slots,
		scheme:         scheme,
		host:           host,
		path:           path,
		mode:           mode,
		headers:        headers,
		paddingRange:   paddingRange,
		meta:           meta,
		realityEnabled: tlsConfigIsReality(tlsConfig),
	}, nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	sessionID := newSessionID()
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

// pickSlot hands out pool members round-robin. All requests belonging to one dial
// must share the member they were opened with: the upload and download halves of
// stream-up/packet-up are paired by session id, and keeping them on a single
// connection preserves their ordering. Callers capture the *transport* the dial
// landed on (slot.get()) and reuse that, not the slot, so a concurrent refresh
// never splits a dial's halves across two connections.
func (c *Client) pickSlot() *transportSlot {
	n := c.slotIdx.Add(1) - 1
	return c.slots[n%uint64(len(c.slots))]
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
	for _, s := range c.slots {
		old := s.ptr.Swap(c.newTransport())
		if old != nil {
			old.CloseIdleConnections()
		}
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

// newSessionID returns a random session id formatted as a dashed UUID string
// (8-4-4-4-12), matching Xray's sessionId = uuid.New().String() (verified against
// XTLS/Xray-core transport/internet/splithttp dialer.go). The server treats it as
// an opaque grouping key; the dashed format keeps it interchangeable with Xray.
func newSessionID() string {
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
