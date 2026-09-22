package v2raygrpc

import "testing"

// lx: SPEC 093 — descriptor names for Xray's service_name forms.
func TestServerDescServiceNameForms(t *testing.T) {
	testCases := []struct{ name, service, stream string }{
		{"TunService", "TunService", "Tun"},
		{"a/b", "a/b", "Tun"}, // old form: upstream behaviour, unescaped
		{"/a/b/Tun", "a/b", "Tun"},
		{"/a/b/Stream", "a/b", "Stream"},
		{"/a b/Tun", "a%20b", "Tun"},
	}
	for _, testCase := range testCases {
		desc := ServerDesc(testCase.name)
		if desc.ServiceName != testCase.service || desc.Streams[0].StreamName != testCase.stream {
			t.Fatalf("service_name %q: got %q/%q, want %q/%q", testCase.name, desc.ServiceName, desc.Streams[0].StreamName, testCase.service, testCase.stream)
		}
	}
}
