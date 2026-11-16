package v2rayxhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestDownloadSettingsParsesXrayNesting(t *testing.T) {
	var opts option.V2RayXHTTPDownloadOptions
	raw := `{"address":"example.com","port":443,"security":"tls","xhttpSettings":{"host":"cdn.example.com","path":"/dl","headers":{"X-Route":["cdn"]}}}`
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Address != "example.com" || opts.Port != 443 || opts.Security != "tls" {
		t.Fatalf("wrapper fields lost: %+v", opts)
	}
	if opts.Host != "cdn.example.com" || opts.Path != "/dl" {
		t.Fatalf("nested request fields lost: %+v", opts)
	}
	if got := opts.Headers["X-Route"]; len(got) != 1 || got[0] != "cdn" {
		t.Fatalf("nested headers lost: %+v", opts.Headers)
	}
}

func TestDownloadSettingsParsesSnakeNesting(t *testing.T) {
	var opts option.V2RayXHTTPDownloadOptions
	raw := `{"xhttp_settings":{"host":"cdn.example.com","path":"/dl"}}`
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Host != "cdn.example.com" || opts.Path != "/dl" {
		t.Fatalf("snake_case nesting not honoured: %+v", opts)
	}
}

func TestBuildDownloadConfigAcceptsSameEndpointOverrides(t *testing.T) {
	server := M.ParseSocksaddr("example.com:443")
	cfg, err := buildDownloadConfig(
		&option.V2RayXHTTPDownloadOptions{Host: "cdn.example.com", Path: "dl"},
		server,
		true,
		"example.com",
		"/xhttp",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.host != "cdn.example.com" {
		t.Fatalf("host = %q", cfg.host)
	}
	if cfg.path != "/dl" {
		t.Fatalf("path = %q, want the leading slash normalized", cfg.path)
	}
}

func TestBuildDownloadConfigRejectsAsymmetricEndpoint(t *testing.T) {
	server := M.ParseSocksaddr("example.com:443")
	cases := map[string]*option.V2RayXHTTPDownloadOptions{
		"address":  {Address: "other.example"},
		"port":     {Port: 8443},
		"security": {Security: "none"},
		"unknown":  {Security: "quic"},
	}
	for name, opts := range cases {
		if _, err := buildDownloadConfig(opts, server, true, "example.com", "/xhttp", nil); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

// The download half carries the alternate Host/path/headers; the upload half and
// every non-asymmetric dial keep the configured ones.
func TestDownloadRequestUsesOverrides(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{100, 100}, metaOptions{})
	c.download = &downloadConfig{
		host:    "cdn.example.com",
		path:    "/dl",
		headers: http.Header{"X-Route": {"cdn"}},
	}

	download, err := c.newDownloadRequest(context.Background(), "sid")
	if err != nil {
		t.Fatal(err)
	}
	if download.Host != "cdn.example.com" {
		t.Fatalf("download Host = %q", download.Host)
	}
	if !strings.HasPrefix(download.URL.Path, "/dl/sid") {
		t.Fatalf("download path = %q, want the override prefix with the session id", download.URL.Path)
	}
	if got := download.Header.Get("X-Route"); got != "cdn" {
		t.Fatalf("download header X-Route = %q", got)
	}

	upload := mustRequest(t, c, http.MethodPost, "sid", "")
	if upload.Host != "example.com" || !strings.HasPrefix(upload.URL.Path, "/xhttp/sid") {
		t.Fatalf("upload request changed: host=%q path=%q", upload.Host, upload.URL.Path)
	}
	if got := upload.Header.Get("X-Route"); got != "" {
		t.Fatalf("upload picked up the download header: %q", got)
	}
}

func TestDownloadRequestDefaultsWithoutSettings(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{100, 100}, metaOptions{})
	req, err := c.newDownloadRequest(context.Background(), "sid")
	if err != nil {
		t.Fatal(err)
	}
	if req.Host != "example.com" || !strings.HasPrefix(req.URL.Path, "/xhttp/sid") {
		t.Fatalf("request = host %q path %q", req.Host, req.URL.Path)
	}
}
