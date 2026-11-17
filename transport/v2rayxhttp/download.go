package v2rayxhttp

import (
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// downloadConfig is the request-shaping override for the download (GET) half of
// packet-up/stream-up sessions. It is what Xray's `downloadSettings` describes:
// a different Host header, path prefix and/or extra headers for the download
// requests, typically to route them through a CDN while the upload keeps going
// to the origin.
type downloadConfig struct {
	host    string
	path    string
	headers http.Header
}

// buildDownloadConfig validates the configured download block against the
// connection this client already has and resolves its request-shaping fields.
//
// The pool binds every stream of one dial to a single TCP+TLS member — the
// upload and download halves are paired by session id and rely on that shared
// ordering — and the dialer/TLS stack is fixed at construction. A download
// block that points at a different address, port or security layer therefore
// cannot be honoured here; it is rejected explicitly instead of silently
// connecting somewhere else.
func buildDownloadConfig(
	options *option.V2RayXHTTPDownloadOptions,
	serverAddr M.Socksaddr,
	tlsEnabled bool,
	defaultHost string,
	defaultPath string,
	defaultHeaders http.Header,
) (*downloadConfig, error) {
	if options == nil {
		return nil, nil
	}
	if options.Address != "" && !strings.EqualFold(options.Address, socksaddrHost(serverAddr)) {
		return nil, E.New("v2ray-xhttp: download_settings.address differs from the server address; asymmetric download endpoints are not supported")
	}
	if options.Port != 0 && options.Port != int(serverAddr.Port) {
		return nil, E.New("v2ray-xhttp: download_settings.port differs from the server port; asymmetric download endpoints are not supported")
	}
	switch options.Security {
	case "":
	case "tls", "reality":
		if !tlsEnabled {
			return nil, E.New("v2ray-xhttp: download_settings.security=", options.Security, " but the transport has no TLS")
		}
	case "none":
		if tlsEnabled {
			return nil, E.New("v2ray-xhttp: download_settings.security=none differs from the transport TLS; asymmetric download endpoints are not supported")
		}
	default:
		return nil, E.New("v2ray-xhttp: unknown download_settings.security: ", options.Security)
	}
	config := &downloadConfig{
		host:    defaultHost,
		path:    defaultPath,
		headers: defaultHeaders,
	}
	if options.Host != "" {
		config.host = options.Host
	}
	if options.Path != "" {
		path := options.Path
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		config.path = path
	}
	if len(options.Headers) > 0 {
		headers := defaultHeaders.Clone()
		if headers == nil {
			headers = make(http.Header)
		}
		for key, value := range options.Headers {
			headers[key] = append([]string(nil), value...)
		}
		config.headers = headers
	}
	return config, nil
}

func socksaddrHost(addr M.Socksaddr) string {
	if addr.Fqdn != "" {
		return addr.Fqdn
	}
	return addr.Addr.String()
}
