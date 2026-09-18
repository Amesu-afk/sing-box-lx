package grpcname

import "testing"

// lx: SPEC 093 §1.1 — the form table, verbatim.
func TestSplit(t *testing.T) {
	testCases := []struct {
		name    string
		service string
		stream  string
		rawPath string
		path    string
	}{
		{"TunService", "TunService", "Tun", "/TunService/Tun", "/TunService/Tun"},
		{"a/b", "a%2Fb", "Tun", "/a%2Fb/Tun", "/a/b/Tun"},
		{"/a/b/Tun", "a/b", "Tun", "/a/b/Tun", "/a/b/Tun"},
		{"/a/b/Stream", "a/b", "Stream", "/a/b/Stream", "/a/b/Stream"},
		{"/a b/Tun", "a%20b", "Tun", "/a%20b/Tun", "/a b/Tun"},
		{"/a/b/Tun|TunMulti", "a/b", "Tun", "/a/b/Tun", "/a/b/Tun"},
		{"/Tun", "", "Tun", "//Tun", "//Tun"},
		{"", "", "Tun", "//Tun", "//Tun"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service, stream := Split(testCase.name)
			if service != testCase.service || stream != testCase.stream {
				t.Fatalf("Split(%q) = (%q, %q), want (%q, %q)", testCase.name, service, stream, testCase.service, testCase.stream)
			}
			if rawPath := RawPath(testCase.name); rawPath != testCase.rawPath {
				t.Fatalf("RawPath(%q) = %q, want %q", testCase.name, rawPath, testCase.rawPath)
			}
			if path := Path(testCase.name); path != testCase.path {
				t.Fatalf("Path(%q) = %q, want %q", testCase.name, path, testCase.path)
			}
		})
	}
}

func TestIsCustom(t *testing.T) {
	for name, want := range map[string]bool{
		"TunService": false,
		"a/b":        false,
		"":           false,
		"/a/b/Tun":   true,
		"/Tun":       true,
		"/":          true,
	} {
		if got := IsCustom(name); got != want {
			t.Fatalf("IsCustom(%q) = %v, want %v", name, got, want)
		}
	}
}
