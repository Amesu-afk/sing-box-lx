package v2rayxhttp

import (
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// defaultPoolSize is the built-in pool width when the config has no xmux
	// block; it is what every profile written before xmux existed runs on.
	defaultPoolSize = transportPoolSize
	// maxPoolSize bounds max_connections. More members than this buys nothing —
	// each one is a separate TCP+TLS session — and every extra socket costs a
	// handshake and radio wakeup on mobile.
	maxPoolSize = 32
)

// xmuxConfig is the parsed form of [option.V2RayXHTTPXMuxOptions]. Zero values
// mean "built-in behaviour", so a client built from a config without xmux runs
// exactly as it did before the block existed.
type xmuxConfig struct {
	poolSize       int
	maxConcurrency int
	reuseTimes     int64
	maxReusableAge time.Duration
}

func newXMuxConfig(options *option.V2RayXHTTPXMuxOptions) (xmuxConfig, error) {
	cfg := xmuxConfig{poolSize: defaultPoolSize}
	if options == nil {
		return cfg, nil
	}
	if options.MaxConnections > 0 {
		cfg.poolSize = min(max(options.MaxConnections, 1), maxPoolSize)
	}
	if raw := strings.TrimSpace(options.MaxConcurrency); raw != "" {
		parsed, err := parseRange(raw, "xmux.max_concurrency")
		if err != nil {
			return cfg, E.New("v2ray-xhttp: ", err)
		}
		if parsed.min < 0 {
			return cfg, E.New("v2ray-xhttp: xmux.max_concurrency must not be negative")
		}
		cfg.maxConcurrency = parsed.rand()
	}
	if options.CMaxReuseTimes > 0 {
		cfg.reuseTimes = int64(options.CMaxReuseTimes)
	}
	if raw := strings.TrimSpace(options.HMaxRequestTimes); raw != "" {
		parsed, err := parseRange(raw, "xmux.h_max_request_times")
		if err != nil {
			return cfg, E.New("v2ray-xhttp: ", err)
		}
		if parsed.min < 0 {
			return cfg, E.New("v2ray-xhttp: xmux.h_max_request_times must not be negative")
		}
		// One member serves one stream per proxied connection, so "requests per
		// connection" and "streams per connection" are the same counter here;
		// the smaller of the two limits wins, as in Xray.
		limit := int64(parsed.rand())
		if limit > 0 && (cfg.reuseTimes == 0 || limit < cfg.reuseTimes) {
			cfg.reuseTimes = limit
		}
	}
	if raw := strings.TrimSpace(options.HMaxReusableSecs); raw != "" {
		parsed, err := parseRange(raw, "xmux.h_max_reusable_secs")
		if err != nil {
			return cfg, E.New("v2ray-xhttp: ", err)
		}
		if parsed.min < 0 {
			return cfg, E.New("v2ray-xhttp: xmux.h_max_reusable_secs must not be negative")
		}
		cfg.maxReusableAge = time.Duration(parsed.rand()) * time.Second
	}
	return cfg, nil
}

// applyXMuxConfig stores the parsed pool tuning and sizes the pool. It runs in
// NewClient after the client skeleton exists, replacing the default-width slots
// the constructor built — they hold no sockets yet, so nothing is leaked.
func (c *Client) applyXMuxConfig(cfg xmuxConfig) {
	c.poolSize = cfg.poolSize
	c.maxConcurrency = int64(cfg.maxConcurrency)
	c.reuseTimes = cfg.reuseTimes
	c.maxReusableAge = cfg.maxReusableAge
	slots := make([]*transportSlot, cfg.poolSize)
	for i := range slots {
		s := &transportSlot{}
		s.ptr.Store(c.newPoolTransport())
		slots[i] = s
	}
	c.refreshMu.Lock()
	c.slots = slots
	c.refreshMu.Unlock()
}

// retireMemberOnLimit swaps a member out because it hit a reuse or age limit.
// Routine rotation, kept apart from retireMember so a timeout failure and a
// planned retirement do not share one misleading log line.
func (c *Client) retireMemberOnLimit(member *poolTransport) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if !member.retired.CompareAndSwap(false, true) {
		return
	}
	for _, s := range c.slots {
		if s.get() != member {
			continue
		}
		s.ptr.Store(c.newPoolTransport())
		c.memberRefreshes.Add(1)
		if c.plog != nil {
			c.plog.Info("xhttp-pool: member retired at its reuse/age limit")
		}
		return
	}
}
