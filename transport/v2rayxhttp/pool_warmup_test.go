package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// A replaced member must prove its path before live traffic lands on it:
// the next dial otherwise lands on the least-loaded (fresh, cold) slot and
// pays the full cold budget on user traffic.
func TestRetiredMemberIsWarmedInBackground(t *testing.T) {
	c := newTestClient(1)
	c.ctx = context.Background()
	c.warmReplacements = true
	c.scheme, c.host, c.path = "http", "warm.test", "/warm/"
	c.serverAddr = M.ParseSocksaddr("warm.test:80")
	var err error
	if c.meta, err = normalizeMeta(metaOptions{}, modePacketUp); err != nil {
		t.Fatal(err)
	}

	probed := make(chan struct{}, 1)
	var fresh *poolTransport
	c.newTransport = func() *poolTransport {
		fresh = &poolTransport{rt: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Register a socket like the real HTTP/2 dial hook does, so the
			// probe promotes the member to warm through the normal path.
			local, _ := net.Pipe()
			_ = fresh.trackConnection(local)
			select {
			case probed <- struct{}{}:
			default:
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		return fresh
	}

	old := c.slots[0].get()
	c.retireTransport(old)

	select {
	case <-probed:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement member was not probed in the background")
	}
	if got := c.slots[0].get(); got == old {
		t.Fatal("timed-out member was not replaced")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c.slots[0].get().warm.Load() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement member stayed cold after a successful probe")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A failed probe must leave the member cold — exactly the no-worse-than-before
// state — and must not disturb the pool.
func TestFailedWarmupLeavesMemberCold(t *testing.T) {
	c := newTestClient(1)
	c.ctx = context.Background()
	c.warmReplacements = true
	c.scheme, c.host, c.path = "http", "warm.test", "/warm/"
	c.serverAddr = M.ParseSocksaddr("warm.test:80")
	var err error
	if c.meta, err = normalizeMeta(metaOptions{}, modePacketUp); err != nil {
		t.Fatal(err)
	}
	c.newTransport = func() *poolTransport {
		return &poolTransport{rt: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		})}
	}

	old := c.slots[0].get()
	c.retireTransport(old)
	fresh := c.slots[0].get()
	if fresh == old {
		t.Fatal("timed-out member was not replaced")
	}
	time.Sleep(200 * time.Millisecond) // let the background probe fail
	if fresh.warm.Load() {
		t.Fatal("member went warm on a failed probe")
	}
	if got := c.memberRefreshes.Load(); got != 1 {
		t.Fatalf("memberRefreshes = %d, want 1", got)
	}
}
