package v2rayxhttp

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

func TestNewXMuxConfigDefaults(t *testing.T) {
	cfg, err := newXMuxConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.poolSize != defaultPoolSize {
		t.Fatalf("poolSize = %d, want %d", cfg.poolSize, defaultPoolSize)
	}
	if cfg.maxConcurrency != 0 || cfg.reuseTimes != 0 || cfg.maxReusableAge != 0 {
		t.Fatalf("zero-value limits expected, got %+v", cfg)
	}
}

func TestNewXMuxConfigParsesRangesAndCaps(t *testing.T) {
	cfg, err := newXMuxConfig(&option.V2RayXHTTPXMuxOptions{
		MaxConnections:   4,
		MaxConcurrency:   "3-3",
		CMaxReuseTimes:   10,
		HMaxRequestTimes: "6-6",
		HMaxReusableSecs: "60-60",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.poolSize != 4 {
		t.Fatalf("poolSize = %d, want 4", cfg.poolSize)
	}
	if cfg.maxConcurrency != 3 {
		t.Fatalf("maxConcurrency = %d, want 3", cfg.maxConcurrency)
	}
	// Both limits bound the same counter here; the smaller one wins.
	if cfg.reuseTimes != 6 {
		t.Fatalf("reuseTimes = %d, want 6 (min of c_max_reuse_times and h_max_request_times)", cfg.reuseTimes)
	}
	if cfg.maxReusableAge != time.Minute {
		t.Fatalf("maxReusableAge = %v, want 1m", cfg.maxReusableAge)
	}
}

func TestNewXMuxConfigBoundsPoolSize(t *testing.T) {
	cfg, err := newXMuxConfig(&option.V2RayXHTTPXMuxOptions{MaxConnections: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.poolSize != maxPoolSize {
		t.Fatalf("poolSize = %d, want the %d cap", cfg.poolSize, maxPoolSize)
	}
	cfg, err = newXMuxConfig(&option.V2RayXHTTPXMuxOptions{MaxConnections: 0})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.poolSize != defaultPoolSize {
		t.Fatalf("poolSize = %d, want the default %d", cfg.poolSize, defaultPoolSize)
	}
}

func TestNewXMuxConfigRejectsBadRanges(t *testing.T) {
	if _, err := newXMuxConfig(&option.V2RayXHTTPXMuxOptions{MaxConcurrency: "x-y"}); err == nil {
		t.Fatal("expected a parse error for a non-numeric range")
	}
	// Reversed bounds are accepted and normalized, matching parseRange's
	// behaviour elsewhere in this transport.
	cfg, err := newXMuxConfig(&option.V2RayXHTTPXMuxOptions{HMaxRequestTimes: "10-1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.reuseTimes < 1 || cfg.reuseTimes > 10 {
		t.Fatalf("reuseTimes = %d, want within the normalized 1..10 range", cfg.reuseTimes)
	}
}

// The cap is soft: a member below it is always a better target than one over it,
// even when the over-cap member is otherwise the least loaded.
func TestAcquireSlotPrefersBelowConcurrencyCap(t *testing.T) {
	c := newTestClient(4)
	c.maxConcurrency = 1
	c.slots[0].get().inflight.Store(2)
	c.slots[1].get().inflight.Store(2)
	c.slots[2].get().inflight.Store(0)
	c.slots[3].get().inflight.Store(2)

	lease := c.acquireSlot()
	if lease.rt != c.slots[2].get() {
		t.Fatalf("acquired a member over the concurrency cap")
	}
	lease.release()
}

// Reaching the reuse limit retires the member: the slot must point at a fresh
// transport shortly after the stream that hit the limit ended.
func TestReuseLimitRetiresMember(t *testing.T) {
	c := newTestClient(2)
	c.reuseTimes = 1

	lease := c.acquireSlot()
	member := lease.rt
	lease.release()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		replaced := true
		for _, s := range c.slots {
			if s.get() == member {
				replaced = false
			}
		}
		if replaced {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("member was not replaced after reaching the reuse limit")
}

// An expired member is still served for the dial that notices the age, but it has
// to leave the pool so later dials land on a fresh connection.
func TestExpiredMemberIsRetired(t *testing.T) {
	c := newTestClient(2)
	c.maxReusableAge = time.Nanosecond
	// Backdate the members: on Windows two time.Now() calls in one tick can be
	// equal, and an age of exactly zero is not expired yet.
	for _, s := range c.slots {
		s.get().createdAt = time.Now().Add(-time.Minute)
	}

	lease := c.acquireSlot()
	member := lease.rt
	lease.release()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		replaced := true
		for _, s := range c.slots {
			if s.get() == member {
				replaced = false
			}
		}
		if replaced {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expired member was not replaced")
}
