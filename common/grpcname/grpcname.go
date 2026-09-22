// Package grpcname implements Xray's gRPC `serviceName` convention: a leading
// "/" turns the value into a custom path whose segments are escaped one by one
// and whose last segment is the stream name.
//
// lx: SPEC 093 — SPECS/TASKS/093-GRPC_SERVICE_NAME_CUSTOM_PATH/SPEC.md.
// Ported verbatim from Xray's transport/internet/grpc/config.go
// (getServiceName / getTunStreamName).
package grpcname

import (
	"net/url"
	"strings"
)

// Split returns the already-escaped service and stream parts of serviceName.
//
// Without a leading "/" the whole name is one escaped service segment and the
// stream is "Tun" — byte for byte what the transports produced before SPEC 093.
// With a leading "/" the name is a custom path: everything up to the last "/"
// is the service (escaped per segment), the tail is the stream name, and a
// "|<multi>" suffix on the stream is dropped the way Xray drops it.
func Split(serviceName string) (service, stream string) {
	if !strings.HasPrefix(serviceName, "/") {
		return url.PathEscape(serviceName), "Tun"
	}
	lastIndex := strings.LastIndex(serviceName, "/")
	if lastIndex < 1 {
		lastIndex = 1
	}
	rawServiceName := serviceName[1:lastIndex]
	parts := strings.Split(rawServiceName, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	service = strings.Join(parts, "/")
	endingPath := serviceName[strings.LastIndex(serviceName, "/")+1:]
	stream = url.PathEscape(strings.Split(endingPath, "|")[0])
	return service, stream
}

// RawPath is the escaped request path that goes on the wire.
func RawPath(serviceName string) string {
	service, stream := Split(serviceName)
	return "/" + service + "/" + stream
}

// Path is the decoded form of RawPath, used for url.URL.Path and for the
// server-side comparison against request.URL.Path. An undecodable RawPath is
// returned as is.
func Path(serviceName string) string {
	raw := RawPath(serviceName)
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
}

// IsCustom reports whether serviceName uses the custom-path form.
func IsCustom(serviceName string) bool {
	return strings.HasPrefix(serviceName, "/")
}
