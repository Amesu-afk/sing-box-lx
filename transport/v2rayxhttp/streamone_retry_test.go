package v2rayxhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stream-one dial that lands on a post-idle zombie must not fail after the
// full warm budget: the pre-headers upload is only the handshake, so it is
// replayed on a fresh member and the dial succeeds on the second attempt.
func TestDialStreamOneRetriesStaleMember(t *testing.T) {
	restoreWarm := swapHandshakeTimeout(50 * time.Millisecond)
	defer restoreWarm()
	restoreFresh := swapFreshHandshakeTimeout(50 * time.Millisecond)
	defer restoreFresh()

	const handshake = "vless-handshake-bytes"
	zombie := &poolTransport{rt: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	var mu sync.Mutex
	var received []byte
	good := &poolTransport{rt: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Like a real server: read the handshake prefix, then answer while
		// the upload side stays open for the rest of the stream. ReadAll
		// would never return on a streaming body.
		header := make([]byte, len(handshake))
		if _, err := io.ReadFull(req.Body, header); err != nil {
			return nil, err
		}
		mu.Lock()
		received = header
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("downlink"))}, nil
	})}

	c := newTestClient(2)
	c.slots[0].ptr.Store(zombie)
	c.slots[1].ptr.Store(good)

	conn, err := c.dialStreamOne(context.Background(), "")
	if err != nil {
		t.Fatalf("dialStreamOne returned an error synchronously: %v", err)
	}
	defer conn.Close()

	// The VLESS layer writes immediately, before headers arrive — exactly the
	// write that used to be lost with the dial.
	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte(handshake))
		writeErr <- err
	}()

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read after stale-member retry: %v", err)
	}
	if string(buf[:n]) != "downlink" {
		t.Fatalf("downlink = %q, want %q", buf[:n], "downlink")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("handshake Write released by the rotation must succeed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if string(received) != handshake {
		t.Fatalf("server received %q, want exactly %q (loss or duplication)", received, handshake)
	}
	if c.slots[0].get() == zombie {
		t.Fatal("timed-out member was not retired from its slot")
	}
	if got := c.memberRefreshes.Load(); got != 1 {
		t.Fatalf("memberRefreshes = %d, want 1", got)
	}
}

// When the replacement member is dead too the dial must still fail — with the
// second timeout, not a loop — and surface it on both halves of the conn.
func TestDialStreamOneFailsWhenReplacementAlsoStale(t *testing.T) {
	restoreWarm := swapHandshakeTimeout(30 * time.Millisecond)
	defer restoreWarm()
	restoreFresh := swapFreshHandshakeTimeout(30 * time.Millisecond)
	defer restoreFresh()

	zombieRT := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	c := newTestClient(2)
	c.slots[0].ptr.Store(&poolTransport{rt: zombieRT})
	c.slots[1].ptr.Store(&poolTransport{rt: zombieRT})

	conn, err := c.dialStreamOne(context.Background(), "")
	if err != nil {
		t.Fatalf("dialStreamOne returned an error synchronously: %v", err)
	}
	defer conn.Close()

	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("handshake"))
		writeErr <- err
	}()

	buf := make([]byte, 64)
	if _, err := conn.Read(buf); err != errHandshakeTimeout {
		t.Fatalf("Read = %v, want errHandshakeTimeout", err)
	}
	// The handshake Write itself was accepted into the replay buffer before
	// the dial died; the failure surfaces on Read, which is what the relay
	// reacts to by reopening the connection.
	if err := <-writeErr; err != nil {
		t.Fatalf("handshake Write into the replay buffer must succeed: %v", err)
	}
	if got := c.memberRefreshes.Load(); got != 2 {
		t.Fatalf("memberRefreshes = %d, want 2 (both members retired)", got)
	}
}

// Rotation must replay exactly the bytes written so far — no loss, no
// duplication — and later writes must flow live to the new pipe.
func TestUploadRecorderRotationReplaysExactlyOnce(t *testing.T) {
	pr, pw := io.Pipe()
	rec := newUploadRecorder(pw, pr)

	writeErr := make(chan error, 1)
	go func() {
		_, err := rec.Write([]byte("hello"))
		writeErr <- err
	}()
	// Let the write reach the (unread) pipe and block.
	time.Sleep(50 * time.Millisecond)

	snapshot, newReader, _ := rec.rotate()
	if string(snapshot) != "hello" {
		t.Fatalf("snapshot = %q, want %q", snapshot, "hello")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write released by rotation: %v", err)
	}

	readAll := make(chan []byte, 1)
	go func() {
		full, _ := io.ReadAll(io.MultiReader(bytes.NewReader(snapshot), newReader))
		readAll <- full
	}()
	if _, err := rec.Write([]byte(" world")); err != nil {
		t.Fatalf("post-rotation write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("recorder Close: %v", err)
	}
	select {
	case full := <-readAll:
		if string(full) != "hello world" {
			t.Fatalf("replay = %q, want %q", full, "hello world")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replay body never completed")
	}
}

// Once the response headers are in, no retry can happen: discard must stop the
// recorder rather than only clearing the prefix, or a long-lived stream keeps
// appending every upload byte to memory for its whole life.
func TestUploadRecorderDiscardStopsRecording(t *testing.T) {
	pr, pw := io.Pipe()
	rec := newUploadRecorder(pw, pr)
	read := make(chan []byte, 1)
	go func() {
		payload := make([]byte, len("after-discard"))
		_, _ = io.ReadFull(pr, payload)
		read <- payload
	}()

	rec.discard()
	if _, err := rec.Write([]byte("after-discard")); err != nil {
		t.Fatalf("post-discard write: %v", err)
	}
	if got := <-read; string(got) != "after-discard" {
		t.Fatalf("post-discard payload = %q, want %q", got, "after-discard")
	}
	rec.mu.Lock()
	retained := len(rec.buf)
	rec.mu.Unlock()
	if retained != 0 {
		t.Fatalf("recorder retained %d bytes after discard", retained)
	}
}

// A real break (deadline, dial cancel) must fail blocked writes: the stream is
// being torn down, not moved. A write released by the rotation itself still
// succeeds - its bytes ride the replay.
func TestUploadRecorderBreakWinsOverRotation(t *testing.T) {
	pr, pw := io.Pipe()
	rec := newUploadRecorder(pw, pr)

	firstErr := make(chan error, 1)
	go func() {
		_, err := rec.Write([]byte("hello"))
		firstErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	rec.rotate()
	if err := <-firstErr; err != nil {
		t.Fatalf("write released by rotation: %v", err)
	}

	// A write into the live tail blocks; a real break must fail it.
	secondErr := make(chan error, 1)
	go func() {
		_, err := rec.Write([]byte("more"))
		secondErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	breakErr := context.DeadlineExceeded
	rec.breakWrites(breakErr)
	select {
	case err := <-secondErr:
		if err != breakErr {
			t.Fatalf("blocked write = %v, want the break error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked write was not released by breakWrites")
	}
}
