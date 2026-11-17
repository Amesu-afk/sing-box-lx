package mobile

import (
	"context"
	"testing"
	"time"

	"github.com/openlibrecommunity/olcrtc/internal/control"
)

func TestStartWithTransportAllowsV1WithoutExplicitDeviceID(t *testing.T) {
	// A v1 URI has no client_id. StartWithTransport must pass the empty value to
	// client.RunWithReady, which creates a DeviceID instead of rejecting the profile up front.
	if err := validateStartArguments("jitsi", "room", "key"); err != nil {
		t.Fatalf("v1 arguments rejected: %v", err)
	}
}

func TestStartWithTransportStillRequiresConnectionFields(t *testing.T) {
	if err := validateStartArguments("", "room", "key"); err == nil {
		t.Fatal("missing provider accepted")
	}
	if err := validateStartArguments("jitsi", "", "key"); err == nil {
		t.Fatal("missing room accepted")
	}
	if err := validateStartArguments("jitsi", "room", ""); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestControlRTTOnlyReportsFreshPongsFromCurrentSession(t *testing.T) {
	mu.Lock()
	previousCancel, previousRTT, previousPong := cancel, controlRTTMillis, controlLastPong
	previousSession, previousReconnects := controlSessionID, controlReconnects
	_, fakeCancel := context.WithCancel(context.Background())
	cancel = fakeCancel
	controlRTTMillis = -1
	controlLastPong = time.Time{}
	controlSessionID = ""
	controlReconnects = 0
	mu.Unlock()
	defer func() {
		fakeCancel()
		mu.Lock()
		cancel, controlRTTMillis, controlLastPong = previousCancel, previousRTT, previousPong
		controlSessionID, controlReconnects = previousSession, previousReconnects
		mu.Unlock()
	}()

	if got := GetControlRTTMillis(); got != -1 {
		t.Fatalf("before pong: got %d, want -1", got)
	}
	now := time.Now()
	recordControlHealth(control.Status{SessionID: "one"})
	recordControlHealth(control.Status{SessionID: "one", LastPong: now, LastRTT: 42 * time.Millisecond})
	if got := GetControlRTTMillis(); got != 42 {
		t.Fatalf("fresh pong: got %d, want 42", got)
	}
	recordControlHealth(control.Status{SessionID: "one", Reconnects: 1, LastPong: now, LastRTT: 42 * time.Millisecond})
	if got := GetControlRTTMillis(); got != -1 {
		t.Fatalf("during reconnect: got %d, want -1", got)
	}
	next := now.Add(time.Millisecond)
	recordControlHealth(control.Status{SessionID: "two", Reconnects: 1, LastPong: now, LastRTT: 42 * time.Millisecond})
	recordControlHealth(control.Status{SessionID: "two", Reconnects: 1, LastPong: next, LastRTT: 83 * time.Millisecond})
	if got := GetControlRTTMillis(); got != 83 {
		t.Fatalf("new session pong: got %d, want 83", got)
	}
	mu.Lock()
	controlLastPong = time.Now().Add(-time.Minute)
	mu.Unlock()
	if got := GetControlRTTMillis(); got != -1 {
		t.Fatalf("stale pong: got %d, want -1", got)
	}
}
