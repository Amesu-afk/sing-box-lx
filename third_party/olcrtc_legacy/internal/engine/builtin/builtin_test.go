package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/openlibrecommunity/olcrtc/internal/auth"
)

func TestTelemostCarrierRegistered(t *testing.T) {
	RegisterDefaults()
	_, err := Open(context.Background(), "telemost", Config{})
	if !errors.Is(err, auth.ErrRoomIDRequired) {
		t.Fatalf("Open(telemost) error = %v, want room ID required", err)
	}
}
