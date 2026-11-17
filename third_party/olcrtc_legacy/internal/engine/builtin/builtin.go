// Package builtin wires the legacy carriers needed by mobile profiles.
// Keeping this registry narrow avoids pulling desktop subscription storage
// and unrelated server code into the Android AAR.
package builtin

import (
	"context"
	"fmt"

	"github.com/openlibrecommunity/olcrtc/internal/auth"
	authJitsi "github.com/openlibrecommunity/olcrtc/internal/auth/jitsi"
	authTelemost "github.com/openlibrecommunity/olcrtc/internal/auth/telemost"
	"github.com/openlibrecommunity/olcrtc/internal/engine"
	_ "github.com/openlibrecommunity/olcrtc/internal/engine/goolom"
	_ "github.com/openlibrecommunity/olcrtc/internal/engine/jitsi"
)

// Config is the subset consumed by the transport factories.
type Config struct {
	RoomURL    string
	Name       string
	OnData     func([]byte)
	OnPeerData func(peerID string, data []byte)
	DNSServer  string
	ProxyAddr  string
	ProxyPort  int
	Insecure   bool
	Engine     string
	URL        string
	Token      string
}

type Factory func(context.Context, Config) (engine.Session, error)

var registry = map[string]Factory{}

func Register(name string, factory Factory) { registry[name] = factory }

func Open(ctx context.Context, name string, cfg Config) (engine.Session, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("carrier not found: %q", name)
	}
	return factory(ctx, cfg)
}

// RegisterDefaults installs the manager-compatible carriers.
func RegisterDefaults() {
	Register("jitsi", func(ctx context.Context, cfg Config) (engine.Session, error) {
		credentials, err := (authJitsi.Provider{}).Issue(ctx, auth.Config{
			RoomURL: cfg.RoomURL,
			Name: cfg.Name,
			DNSServer: cfg.DNSServer,
			ProxyAddr: cfg.ProxyAddr,
			ProxyPort: cfg.ProxyPort,
			Insecure: cfg.Insecure,
		})
		if err != nil {
			return nil, err
		}
		sess, err := engine.New(ctx, "jitsi", engine.Config{
			URL: credentials.URL,
			Token: credentials.Token,
			Name: cfg.Name,
			Extra: credentials.Extra,
			OnData: cfg.OnData,
			OnPeerData: cfg.OnPeerData,
			DNSServer: cfg.DNSServer,
			ProxyAddr: cfg.ProxyAddr,
			ProxyPort: cfg.ProxyPort,
		})
		if err != nil {
			return nil, fmt.Errorf("engine new: %w", err)
		}
		return sess, nil
	})
	Register("telemost", func(ctx context.Context, cfg Config) (engine.Session, error) {
		provider := authTelemost.Provider{}
		issue := func(ctx context.Context) (auth.Credentials, error) {
			return provider.Issue(ctx, auth.Config{
				RoomURL: cfg.RoomURL,
				Name: cfg.Name,
				DNSServer: cfg.DNSServer,
				ProxyAddr: cfg.ProxyAddr,
				ProxyPort: cfg.ProxyPort,
				Insecure: cfg.Insecure,
			})
		}
		credentials, err := issue(ctx)
		if err != nil {
			return nil, err
		}
		return engine.New(ctx, provider.Engine(), engine.Config{
			URL: credentials.URL,
			Token: credentials.Token,
			Name: cfg.Name,
			Extra: credentials.Extra,
			OnData: cfg.OnData,
			OnPeerData: cfg.OnPeerData,
			DNSServer: cfg.DNSServer,
			ProxyAddr: cfg.ProxyAddr,
			ProxyPort: cfg.ProxyPort,
			Refresh: func(ctx context.Context) (engine.Credentials, error) {
				fresh, err := issue(ctx)
				return engine.Credentials{URL: fresh.URL, Token: fresh.Token, Extra: fresh.Extra}, err
			},
		})
	})
}
