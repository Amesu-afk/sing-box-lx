//go:build with_utls

// lx: SPECS/TASKS/092-INIT_ERROR_NAMES_TAG
//
// A config rejected by box.New must name the offending element by type and tag,
// not by index alone. LxBox and the launcher show the core's text verbatim, and
// "initialize outbound[0]: invalid short_id" tells a user with a hundred-node
// subscription nothing — the nodes carry tags, not numbers. This stand pins the
// wrapper text of the six indexed init loops in box.go: the upstream prefix
// "initialize <kind>[<i>]" stays byte-for-byte (parsers may match it) and the
// new tail " <type>[<tag>]" lands before the colon, mirroring the logger name.
//
// Lives here rather than in test/ for the same reason as lx-test/startclose and
// lx-test/zombie: the test/ module resolves the fork submodules from the proxy
// and does not build.
package initerr

import (
	"context"
	"testing"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// A valid 32-byte base64url key: the REALITY client checks public_key before
// short_id, so the key must pass for the short_id error to be the one reported.
const realityPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// 18 hex characters — over the 16-character (8-byte) limit.
const badShortID = "0123456789abcdef01"

func realityVLESSOutbound(tag string) option.Outbound {
	return option.Outbound{
		Type: C.TypeVLESS,
		Tag:  tag,
		Options: &option.VLESSOutboundOptions{
			ServerOptions: option.ServerOptions{
				Server:     "127.0.0.1",
				ServerPort: 443,
			},
			UUID: "b831381d-6324-4d53-ad4f-8cda48b30811",
			OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
				TLS: &option.OutboundTLSOptions{
					Enabled:    true,
					ServerName: "example.com",
					UTLS: &option.OutboundUTLSOptions{
						Enabled:     true,
						Fingerprint: "chrome",
					},
					Reality: &option.OutboundRealityOptions{
						Enabled:   true,
						PublicKey: realityPublicKey,
						ShortID:   badShortID,
					},
				},
			},
		},
	}
}

func newBox(t *testing.T, options option.Options) error {
	t.Helper()
	ctx := include.Context(context.Background())
	options.Log = &option.LogOptions{Disabled: true}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err == nil {
		require.NoError(t, instance.Close())
		t.Fatal("expected box.New to fail on a broken config")
	}
	return err
}

func TestInitErrorNamesOutboundWithTag_LX(t *testing.T) {
	err := newBox(t, option.Options{
		Outbounds: []option.Outbound{realityVLESSOutbound("proxy-de-1")},
	})
	require.Contains(t, err.Error(), "initialize outbound[0] vless[proxy-de-1]: ")
	require.Contains(t, err.Error(), "invalid short_id")
}

func TestInitErrorNamesOutboundWithoutTag_LX(t *testing.T) {
	err := newBox(t, option.Options{
		Outbounds: []option.Outbound{realityVLESSOutbound("")},
	})
	require.Contains(t, err.Error(), "initialize outbound[0] vless[0]: ")
	require.Contains(t, err.Error(), "invalid short_id")
}

func TestInitErrorNamesInbound_LX(t *testing.T) {
	// A server TLS config with no certificate, no key and no insecure flag
	// fails inside the mixed inbound's constructor, i.e. at Create time.
	err := newBox(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "in-local",
				Options: &option.HTTPMixedInboundOptions{
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{Enabled: true},
					},
				},
			},
		},
	})
	require.Contains(t, err.Error(), "initialize inbound[0] mixed[in-local]: ")
	require.Contains(t, err.Error(), "missing certificate")
}
