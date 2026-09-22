// lx: SPECS/TASKS/091 §1 — `udp_relay_mode` must reject a typo at load time
// instead of silently meaning `native`. The upstream `switch` had no `default`,
// so "qiuc", "Native" and "udp" all resolved to the native relay while the
// neighbouring `congestion_control` rejects its own typos — the same outbound
// behaved two different ways.
//
// NewOutbound is reachable with a bare context: everything it touches before
// the relay-mode switch (TLS client, uuid parse) needs no services.
package tuic

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func newRelayModeOptions(mode string) option.TUICOutboundOptions {
	return option.TUICOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
		UUID:          "00000000-0000-0000-0000-000000000000",
		UDPRelayMode:  mode,
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true},
		},
	}
}

func buildRelayModeOutbound(t *testing.T, options option.TUICOutboundOptions) error {
	t.Helper()
	_, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "tuic-test", options)
	return err
}

// The bug: a typo used to be accepted and mean `native`.
func TestLxTUICUnknownUDPRelayModeRejected(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"qiuc", "Native", "udp", "QUIC"} {
		err := buildRelayModeOutbound(t, newRelayModeOptions(mode))
		if err == nil {
			t.Fatalf("udp_relay_mode %q must be rejected, got no error", mode)
		}
		if !strings.Contains(err.Error(), "unknown udp_relay_mode") {
			t.Fatalf("udp_relay_mode %q: expected an `unknown udp_relay_mode` error, got %v", mode, err)
		}
		// the text has to tell the user what to write instead
		if !strings.Contains(err.Error(), mode) || !strings.Contains(err.Error(), "expected native or quic") {
			t.Fatalf("udp_relay_mode %q: error must name the value and the legal set, got %v", mode, err)
		}
	}
}

// The documented values — including the empty default, which stays `native`.
func TestLxTUICKnownUDPRelayModesAccepted(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "native", "quic"} {
		err := buildRelayModeOutbound(t, newRelayModeOptions(mode))
		if err != nil && strings.Contains(err.Error(), "udp_relay_mode") {
			t.Fatalf("udp_relay_mode %q must pass the relay-mode check, got %v", mode, err)
		}
	}
}

// The pre-existing conflict check keeps priority: two options set against each
// other is a clearer report than a typo inside one of them.
func TestLxTUICUDPOverStreamConflictStillWinsOverTypo(t *testing.T) {
	t.Parallel()
	options := newRelayModeOptions("qiuc")
	options.UDPOverStream = true
	err := buildRelayModeOutbound(t, options)
	if err == nil {
		t.Fatal("udp_over_stream + udp_relay_mode must be rejected")
	}
	if !strings.Contains(err.Error(), "udp_over_stream is conflict with udp_relay_mode") {
		t.Fatalf("the conflict must be reported before the typo, got %v", err)
	}
}
