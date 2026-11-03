package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/openlibrecommunity/olcrtc/internal/handshake"
)

func TestStartupRetryOnlyAcceptsClosedStreams(t *testing.T) {
	for _, err := range []error{io.EOF, fmt.Errorf("read welcome: %w", io.ErrClosedPipe)} {
		if !startupStreamClosed(err) {
			t.Fatalf("closed stream was not retryable: %v", err)
		}
	}
	for _, err := range []error{handshake.ErrRejected, handshake.ErrProtocolVersion, context.Canceled, context.DeadlineExceeded, errors.New("bad key")} {
		if startupStreamClosed(err) {
			t.Fatalf("non-transient failure was retryable: %v", err)
		}
	}
}
