package jitsi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPeerReadinessWaitsForRemoteFrame(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.WaitForPeer(ctx) }()
	select {
	case err := <-result:
		t.Fatalf("local bridge readiness must not start handshake: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	s.peerEpoch.Store(42)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("remote frame did not release startup")
	}
}

func TestPeerReadinessHonorsCancellationAndClose(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.WaitForPeer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup: %v", err)
	}
	close(s.done)
	if err := s.WaitForPeer(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed startup: %v", err)
	}
}
