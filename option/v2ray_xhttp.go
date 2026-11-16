package option

import (
	"encoding/json"

	"github.com/sagernet/sing/common/json/badoption"
)

// V2RayXHTTPOptions configures the XHTTP (Xray "splithttp"/"xhttp") client
// transport. It is a sing-box-lx downstream addition; see SPECS/TASKS/002. JSON keys
// are snake_case to match Xray's stream settings, sing-box-extended and the rest
// of sing-box. See SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/PARAM_MAP.md for the precise
// per-field semantics audited against Xray-core and sing-box-extended.
//
// Modes (Xray-compatible):
//   - "auto"       : Reality → stream-one, otherwise → packet-up.
//   - "packet-up"  : separate GET download stream + sequential POST upload packets.
//   - "stream-up"  : single streamed POST upload + separate GET download stream.
//   - "stream-one" : a single bidirectional HTTP stream (request body up,
//     response body down) — the closest analogue to httpupgrade.
//
// Range fields (uplink_chunk_size, sc_*) are expressed Xray-style as the string
// "min-max" (e.g. "3000-4000") or a single integer ("4000" == "4000-4000"); empty
// selects the documented default. This mirrors x_padding_bytes and avoids adding a
// range type to the option layer.
type V2RayXHTTPOptions struct {
	// ---- v1 fields (unchanged, preserved verbatim) --------------------------

	// Host overrides the HTTP Host header (defaults to the TLS SNI or the
	// server address when empty).
	Host string `json:"host,omitempty"`
	// Path is the request path prefix; the session id (and, for packet-up,
	// the upload sequence number) are appended to it (placement permitting).
	Path string `json:"path,omitempty"`
	// Mode selects the XHTTP transport mode: auto|packet-up|stream-up|stream-one.
	Mode string `json:"mode,omitempty"`
	// Headers are extra request headers sent on every XHTTP request.
	Headers badoption.HTTPHeader `json:"headers,omitempty"`
	// XPaddingBytes is the inclusive byte-length range of the padding value
	// ("min-max", e.g. "100-1000", or a single integer). Empty defaults to
	// "100-1000".
	XPaddingBytes string `json:"x_padding_bytes,omitempty"`
	// NoGRPCHeader omits the "Content-Type: application/grpc" request header that
	// the streamed-body modes (stream-one, stream-up) set by default, mirroring
	// Xray's FillStreamRequest (transport/internet/splithttp/config.go). The
	// header is load-bearing in front of reverse proxies/CDNs, which key
	// response streaming (no buffering) on a gRPC content type — without it a
	// stream-one dial can hang until timeout.
	NoGRPCHeader bool `json:"no_grpc_header,omitempty"`

	// ---- session / seq placement (core) -------------------------------------

	// SessionPlacement selects where the session id is carried on every request:
	// path|query|header|cookie. Empty defaults to "path". (Xray proto field is
	// sessionIDPlacement; we expose session_placement to match sing-box-extended.)
	SessionPlacement string `json:"session_placement,omitempty"`
	// SessionKey is the query param / header / cookie name carrying the session id
	// when placement is not "path". Empty defaults to "X-Session" (header) or
	// "x_session" (query/cookie).
	SessionKey string `json:"session_key,omitempty"`
	// SessionIDTable selects a predefined Xray character table name (for example
	// Base62) or a literal ASCII character set for generated session ids.
	SessionIDTable string `json:"session_id_table,omitempty"`
	// SessionIDLength is the inclusive length range for table-generated session
	// ids. It is used only when SessionIDTable is set; empty preserves UUID ids.
	SessionIDLength string `json:"session_id_length,omitempty"`
	// SeqPlacement selects where the per-packet upload sequence number is carried
	// in packet-up mode: path|query|header|cookie. Empty defaults to "path".
	SeqPlacement string `json:"seq_placement,omitempty"`
	// SeqKey is the name carrying the seq when placement is not "path". Empty
	// defaults to "X-Seq" (header) or "x_seq" (query/cookie).
	SeqKey string `json:"seq_key,omitempty"`

	// ---- uplink data (obfs / tuning) ----------------------------------------

	// UplinkDataPlacement selects where the packet-up upload payload is carried:
	// body|auto|header|cookie. Empty defaults to "auto" (client-equivalent to
	// body). header/cookie are only valid in packet-up mode.
	UplinkDataPlacement string `json:"uplink_data_placement,omitempty"`
	// UplinkDataKey is the base header/cookie name for chunked header/cookie
	// payload. Empty defaults to "X-Data" (header/auto) or "x_data" (cookie).
	UplinkDataKey string `json:"uplink_data_key,omitempty"`
	// UplinkChunkSize is the "min-max" range (in base64 characters) of each
	// header/cookie payload chunk. Empty selects a placement-dependent default
	// (cookie 2048-3072, header 3000-4000, otherwise sc_max_each_post_bytes).
	UplinkChunkSize string `json:"uplink_chunk_size,omitempty"`
	// UplinkHTTPMethod is the HTTP method for upload requests (download is always
	// GET). Empty defaults to "POST"; the value is upper-cased. "GET" is only
	// valid in packet-up mode.
	UplinkHTTPMethod string `json:"uplink_http_method,omitempty"`

	// ---- X-Padding obfs ------------------------------------------------------

	// XPaddingObfsMode is the master switch: false (default) carries padding as
	// "x_padding=<pad>" in a Referer header query (legacy Xray); true uses the
	// configurable x_padding_* fields below.
	XPaddingObfsMode bool `json:"x_padding_obfs_mode,omitempty"`
	// XPaddingKey is the cookie/query parameter name for padding in obfs mode.
	// Empty defaults to "x_padding". Unused when placement is "header".
	XPaddingKey string `json:"x_padding_key,omitempty"`
	// XPaddingHeader is the header name for padding in obfs header/queryInHeader
	// placement. Empty defaults to "X-Padding".
	XPaddingHeader string `json:"x_padding_header,omitempty"`
	// XPaddingPlacement selects where padding is placed in obfs mode:
	// cookie|header|query|queryInHeader. Empty defaults to "queryInHeader".
	XPaddingPlacement string `json:"x_padding_placement,omitempty"`
	// XPaddingMethod selects the padding byte generator: repeat-x|tokenish.
	// Empty defaults to "repeat-x".
	XPaddingMethod string `json:"x_padding_method,omitempty"`

	// ---- packet-up tuning (client-relevant extras) --------------------------

	// ScMaxEachPostBytes is the "min-max" range bounding the size of a single
	// packet-up upload POST (the split threshold). Empty defaults to
	// "1000000-1000000".
	ScMaxEachPostBytes string `json:"sc_max_each_post_bytes,omitempty"`
	// ScMinPostsIntervalMs is the "min-max" range (milliseconds) of the minimum
	// delay between successive packet-up upload POSTs (anti-burst). Empty defaults
	// to "30-30".
	ScMinPostsIntervalMs string `json:"sc_min_posts_interval_ms,omitempty"`

	// ---- pool tuning (xmux) --------------------------------------------------

	// XMux tunes the client connection pool, the counterpart of Xray's `xmux`
	// block for XHTTP. Absent means the built-in defaults, which every config
	// written before this field existed already runs on.
	XMux *V2RayXHTTPXMuxOptions `json:"xmux,omitempty"`

	// DownloadSettings shapes the download (GET) half of packet-up and
	// stream-up sessions — Xray's `downloadSettings`. Absent keeps both halves
	// on the same Host/path/headers.
	DownloadSettings *V2RayXHTTPDownloadOptions `json:"download_settings,omitempty"`

	// ---- legacy / accepted-but-ignored -------------------------------------

	// ScMaxConcurrentPosts is a legacy Xray knob (removed from current Xray-core
	// and sing-box-extended) that once capped concurrent upload POSTs. Current
	// Xray serializes to one POST body in flight at a time (matching our
	// sequential packet-up upload), so this field is accepted for config/link
	// symmetry but IGNORED. See SPECS/TASKS/002 PARAM_MAP.md.
	ScMaxConcurrentPosts int `json:"sc_max_concurrent_posts,omitempty"`

	// ---- server-only (accepted for config symmetry, IGNORED by the client) --

	// ServerMaxHeaderBytes is server-only (http.Server.MaxHeaderBytes). Ignored
	// by the client-only transport; accepted so inbound-shaped configs do not error.
	ServerMaxHeaderBytes int `json:"server_max_header_bytes,omitempty"`
	// NoSSEHeader is server-only (omit Content-Type: text/event-stream on the
	// stream-down response). Ignored by the client (it never inspects Content-Type).
	NoSSEHeader bool `json:"no_sse_header,omitempty"`
	// ScMaxBufferedPosts is server-only (upload reorder buffer depth). Ignored by
	// the client.
	ScMaxBufferedPosts int64 `json:"sc_max_buffered_posts,omitempty"`
	// ScStreamUpServerSecs is server-only ("min-max" keepalive-padding interval on
	// the stream-up response). The client sets no such interval and does not strip
	// server-injected keepalive padding from the stream-up download — verify against
	// the target server if it emits any.
	ScStreamUpServerSecs string `json:"sc_stream_up_server_secs,omitempty"`
}

// V2RayXHTTPXMuxOptions is the client pool tuning block (Xray's `xmux` for
// XHTTP), spelled snake_case like the rest of this option layer. Every field is
// optional; zero or empty keeps the built-in behaviour of a config without
// xmux.
type V2RayXHTTPXMuxOptions struct {
	// MaxConnections caps how many independent HTTP/2 transports (and therefore
	// TCP+TLS sessions) the pool keeps. 0 keeps the default of 8 (bounded to
	// 1..32), matching the size every existing config already runs on.
	MaxConnections int `json:"max_connections,omitempty"`
	// MaxConcurrency is the "min-max" range of live streams one pooled
	// connection may carry before the scheduler prefers a member below the cap.
	// It is a soft cap: when every member is at the limit, the least-loaded one
	// still takes the dial. Empty/0 means no cap.
	MaxConcurrency string `json:"max_concurrency,omitempty"`
	// CMaxReuseTimes retires a pooled member after this many finished streams,
	// so a long session cannot pin one TCP connection forever. 0 = unlimited.
	CMaxReuseTimes int `json:"c_max_reuse_times,omitempty"`
	// HMaxRequestTimes bounds requests per member the same way CMaxReuseTimes
	// bounds streams; when both are set the smaller limit wins. Empty/0 =
	// unlimited.
	HMaxRequestTimes string `json:"h_max_request_times,omitempty"`
	// HMaxReusableSecs retires a member once it is older than this "min-max"
	// range, regardless of load. Empty/0 = unlimited.
	HMaxReusableSecs string `json:"h_max_reusable_secs,omitempty"`
	// HKeepAlivePeriod (seconds) is accepted for Xray config symmetry; keepalive
	// on the pooled sockets stays with the dialer stack, which already probes
	// through the transport's cold/warm handshake budgets.
	HKeepAlivePeriod int `json:"h_keep_alive_period,omitempty"`
}

// V2RayXHTTPDownloadXHTTPSettings is the block Xray nests under
// downloadSettings.xhttpSettings: only the request-shaping fields this client
// uses are represented.
type V2RayXHTTPDownloadXHTTPSettings struct {
	Host    string               `json:"host,omitempty"`
	Path    string               `json:"path,omitempty"`
	Headers badoption.HTTPHeader `json:"headers,omitempty"`
}

// V2RayXHTTPDownloadOptions shapes the download (GET) half of packet-up and
// stream-up sessions, mirroring Xray's `downloadSettings` — typically to route
// downloads through a CDN while the upload keeps going to the origin. The block
// only makes sense for those modes: stream-one carries both directions on one
// stream and ignores it.
//
// Address, port and security are accepted for config compatibility and must
// match the transport itself. The client builds one dialer/TLS stack per
// profile, so a download that points somewhere else is rejected at
// construction rather than silently ignored.
//
// Two shapes are accepted: flat host/path/headers on this object, or the Xray
// nesting under xhttpSettings (also accepted as xhttp_settings). Nested values
// win over flat ones.
type V2RayXHTTPDownloadOptions struct {
	Address  string               `json:"address,omitempty"`
	Port     int                  `json:"port,omitempty"`
	Security string               `json:"security,omitempty"`
	Host     string               `json:"host,omitempty"`
	Path     string               `json:"path,omitempty"`
	Headers  badoption.HTTPHeader `json:"headers,omitempty"`
}

func (o *V2RayXHTTPDownloadOptions) UnmarshalJSON(content []byte) error {
	var raw struct {
		Address       string                           `json:"address"`
		Port          int                              `json:"port"`
		Security      string                           `json:"security"`
		Host          string                           `json:"host"`
		Path          string                           `json:"path"`
		Headers       badoption.HTTPHeader             `json:"headers"`
		SnakeSettings *V2RayXHTTPDownloadXHTTPSettings `json:"xhttp_settings"`
		CamelSettings *V2RayXHTTPDownloadXHTTPSettings `json:"xhttpSettings"`
	}
	// Deliberately lenient (plain encoding/json): Xray configs may carry keys
	// this client does not model, and rejecting the whole block over one of them
	// would make the option unusable exactly where it is meant to be pasted in.
	if err := json.Unmarshal(content, &raw); err != nil {
		return err
	}
	nested := raw.SnakeSettings
	if nested == nil {
		nested = raw.CamelSettings
	}
	*o = V2RayXHTTPDownloadOptions{
		Address:  raw.Address,
		Port:     raw.Port,
		Security: raw.Security,
		Host:     raw.Host,
		Path:     raw.Path,
		Headers:  raw.Headers,
	}
	if nested != nil {
		if nested.Host != "" {
			o.Host = nested.Host
		}
		if nested.Path != "" {
			o.Path = nested.Path
		}
		if len(nested.Headers) > 0 {
			o.Headers = nested.Headers
		}
	}
	return nil
}
