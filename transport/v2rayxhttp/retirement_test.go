package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTP2TimeoutPreservesQuietHealthySiblings(t *testing.T) {
	defer swapHandshakeTimeout(100 * time.Millisecond)()
	client, dialer := recoveryClient(t, 8)
	streams := make([]net.Conn, 8)
	members := make([]*poolTransport, 8)
	for i := range streams {
		streams[i] = recoveryStream(t, client)
		members[i] = client.slots[i].get()
	}
	original := dialer.snapshot()
	original[0].dropWrites.Store(true)
	time.Sleep(150 * time.Millisecond)
	req, err := client.newRequest(context.Background(), http.MethodGet, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.handshakeVia(members[0], req); err != errHandshakeTimeout {
		t.Fatalf("expected warm timeout on member zero, got %v", err)
	}
	client.retireTransport(members[0])
	if !original[0].closed.Load() || client.slots[0].get() == members[0] {
		t.Fatal("failed member was not closed and replaced")
	}
	for i := 1; i < len(streams); i++ {
		if original[i].closed.Load() || client.slots[i].get() != members[i] {
			t.Fatalf("quiet healthy sibling %d was disturbed by another member's timeout", i)
		}
		_ = streams[i].SetDeadline(time.Now().Add(time.Second))
		if _, err := streams[i].Write([]byte{'H'}); err != nil {
			t.Fatal(err)
		}
		var echo [1]byte
		if _, err := io.ReadFull(streams[i], echo[:]); err != nil || echo[0] != 'H' {
			t.Fatalf("quiet healthy sibling %d did not resume: %v", i, err)
		}
	}
}

func TestConcurrentTimeoutsRetireOneMemberOnce(t *testing.T) {
	client := newTestClient(2)
	failed, sibling := client.slots[0].get(), client.slots[1].get()
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client.retireTransport(failed)
		}()
	}
	workers.Wait()
	fresh := client.slots[0].get()
	if fresh == failed || client.slots[1].get() != sibling {
		t.Fatal("concurrent failures did not isolate the failed member")
	}
	client.retireTransport(failed)
	if client.slots[0].get() != fresh {
		t.Fatal("late timeout replaced the fresh member")
	}
}

func TestTimeoutAfterNetworkResetPreservesNewGeneration(t *testing.T) {
	client := newTestClient(2)
	old := client.slots[0].get()
	_ = client.Close()
	fresh := [2]*poolTransport{client.slots[0].get(), client.slots[1].get()}
	client.retireTransport(old)
	for i := range fresh {
		if client.slots[i].get() != fresh[i] {
			t.Fatal("old-network timeout replaced a new-network member")
		}
	}
}

func TestHTTP2ReceivingRetiredMemberRemainsOwnedUntilReset(t *testing.T) {
	client, dialer := recoveryClient(t, 2)
	stream := recoveryStream(t, client)
	failed := client.slots[0].get()
	original := dialer.snapshot()[0]
	client.retireTransport(failed)
	if original.closed.Load() {
		t.Fatal("recently receiving stream was closed on retirement")
	}
	_ = stream.SetDeadline(time.Now().Add(time.Second))
	if _, err := stream.Write([]byte{'R'}); err != nil {
		t.Fatal(err)
	}
	var echo [1]byte
	if _, err := io.ReadFull(stream, echo[:]); err != nil || echo[0] != 'R' {
		t.Fatalf("receiving retired stream could not continue: %v", err)
	}
	_ = client.Close()
	if !original.closed.Load() {
		t.Fatal("network reset lost ownership of the receiving retired socket")
	}
}
