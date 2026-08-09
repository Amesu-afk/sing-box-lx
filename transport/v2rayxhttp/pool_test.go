package v2rayxhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// The pool is balanced by live proxied connections, not by dials. A member that
// has collected long-lived streams must stop being handed new ones while an
// emptier member exists — that drift is what round-robin could not see.
func TestAcquireSlotPrefersLeastLoaded(t *testing.T) {
	c := newTestClient(4)
	c.slots[0].inflight.Store(9)
	c.slots[1].inflight.Store(3)
	c.slots[2].inflight.Store(7)
	c.slots[3].inflight.Store(5)

	lease := c.acquireSlot()
	if lease.slot != c.slots[1] {
		t.Fatalf("acquired a slot other than the least loaded one")
	}
	if got := c.slots[1].inflight.Load(); got != 4 {
		t.Fatalf("inflight = %d, want 4 (the lease must be counted)", got)
	}
	if lease.rt != c.slots[1].get() {
		t.Fatal("lease captured a transport other than the slot's current member")
	}
}

// With nothing held, every member is equally good and the rotating tie-break must
// still spread dials over all of them — otherwise an idle pool would pile every
// new connection onto slot 0 and only recover once it had fallen behind.
func TestAcquireSlotSpreadsOverAnIdlePool(t *testing.T) {
	const size = 4
	c := newTestClient(size)

	seen := make(map[*transportSlot]bool)
	for i := 0; i < size; i++ {
		seen[c.acquireSlot().slot] = true
	}
	if len(seen) != size {
		t.Fatalf("%d distinct members used for %d dials on an idle pool", len(seen), size)
	}
}

// A lease is the only thing that ever gives capacity back, so a double Close (or a
// dial that fails after the conn was handed out) must not decrement twice and make
// a busy member look free.
func TestSlotLeaseReleasesExactlyOnce(t *testing.T) {
	c := newTestClient(1)
	lease := c.acquireSlot()
	if got := c.slots[0].inflight.Load(); got != 1 {
		t.Fatalf("inflight = %d after acquire, want 1", got)
	}
	lease.release()
	lease.release()
	if got := c.slots[0].inflight.Load(); got != 0 {
		t.Fatalf("inflight = %d after two releases, want 0", got)
	}
}

// Closing the proxied connection is what returns the member's capacity. Without
// this the count only ever grows and the pool converges on "everything is equally
// overloaded", i.e. back to round-robin.
func TestConnCloseReturnsCapacity(t *testing.T) {
	c := newTestClient(1)

	streamLease := c.acquireSlot()
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	stream := newStreamConn(pipeWriter, M.Socksaddr{}, streamLease)
	stream.setupReader(io.NopCloser(strings.NewReader("")), nil)
	stream.Close()
	if got := c.slots[0].inflight.Load(); got != 0 {
		t.Fatalf("streamConn.Close left inflight at %d, want 0", got)
	}

	splitLease := c.acquireSlot()
	splitReader, splitWriter := io.Pipe()
	defer splitReader.Close()
	split := newSplitConn(io.NopCloser(strings.NewReader("")), splitWriter, M.Socksaddr{}, splitLease)
	split.Close()
	if got := c.slots[0].inflight.Load(); got != 0 {
		t.Fatalf("splitConn.Close left inflight at %d, want 0", got)
	}

	packetLease := c.acquireSlot()
	packet := newTestPacketConn(t, c, packetLease)
	packet.Close()
	packet.Close() // idempotent: a second Close must not double-release
	if got := c.slots[0].inflight.Load(); got != 0 {
		t.Fatalf("packetConn.Close left inflight at %d, want 0", got)
	}
}

// The regression this pins: a packet-up upload had no bound at all, so a POST that
// landed on a connection the network had dropped blocked until the OS gave up on
// its retransmits — the proxied connection frozen for minutes with no error for
// the app to react to. It must now fail within its budget and take the dead member
// out of the pool.
func TestPacketUploadTimesOutAndRetiresMember(t *testing.T) {
	defer swapUploadTimeout(30 * time.Millisecond)()

	c := newTestClient(1)
	zombie := &recordingTransport{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done() // dropped socket: nothing ever comes back
			return nil, req.Context().Err()
		},
	}
	lease := &slotLease{slot: c.slots[0], rt: &poolTransport{rt: zombie}}
	c.slots[0].inflight.Add(1)
	conn := newTestPacketConn(t, c, lease)
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("payload"))
		done <- err
	}()

	select {
	case err := <-done:
		if err != errHandshakeTimeout {
			t.Fatalf("upload failed with %v, want errHandshakeTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload on a dead connection never returned")
	}
	if got := zombie.closed.Load(); got != 1 {
		t.Fatalf("dead member closed %d times, want exactly 1", got)
	}
}

// Close must unblock an upload already in flight. Otherwise the POST keeps its
// goroutine and its HTTP/2 stream long after the proxied connection is gone, and
// the stream is reclaimed only when the peer or the OS gives up on it.
func TestPacketConnCloseAbortsInFlightUpload(t *testing.T) {
	defer swapUploadTimeout(10 * time.Second)()

	c := newTestClient(1)
	started := make(chan struct{})
	var once atomic.Bool
	blocking := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	lease := &slotLease{slot: c.slots[0], rt: &poolTransport{rt: blocking}}
	c.slots[0].inflight.Add(1)
	conn := newTestPacketConn(t, c, lease)

	done := make(chan struct{})
	go func() {
		conn.Write([]byte("payload"))
		close(done)
	}()

	<-started
	conn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not abort the in-flight upload")
	}
	if got := c.slots[0].inflight.Load(); got != 0 {
		t.Fatalf("inflight = %d after Close, want 0", got)
	}
}

// newTestPacketConn builds a packet-up conn over lease with a discardable download
// side, so the upload path can be exercised without a server.
func newTestPacketConn(t *testing.T, c *Client, lease *slotLease) *packetConn {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{}, modePacketUp)
	if err != nil {
		t.Fatal(err)
	}
	c.meta = meta
	c.scheme = "https"
	c.host = "example"
	c.path = "/upload/"
	c.serverAddr = M.ParseSocksaddr("example:443")
	ctx, cancel := context.WithCancel(context.Background())
	return &packetConn{
		ctx:           ctx,
		cancelUploads: cancel,
		client:        c,
		lease:         lease,
		sessionID:     "session",
		reader:        io.NopCloser(strings.NewReader("")),
		serverAddr:    c.serverAddr,
	}
}

func swapUploadTimeout(d time.Duration) func() {
	old := uploadTimeout
	uploadTimeout = d
	return func() { uploadTimeout = old }
}
