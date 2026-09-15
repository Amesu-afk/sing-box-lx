//go:build with_utls

package tls

import (
	"net"
	"testing"

	utls "github.com/metacubex/utls"
)

func TestEnsureRealityHybridKeyShareFirefox(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	helloSpec, err := utls.UTLSIdToSpec(utls.HelloFirefox_Auto)
	if err != nil {
		t.Fatal(err)
	}
	ensureRealityHybridKeyShare(helloSpec.Extensions)
	uConn := utls.UClient(clientConn, &utls.Config{ServerName: "example.com"}, utls.HelloCustom)
	if err := uConn.ApplyPreset(&helloSpec); err != nil {
		t.Fatal(err)
	}
	if err := uConn.BuildHandshakeState(); err != nil {
		t.Fatal(err)
	}

	assertRealityHybridBeforeX25519(t, uConn.Extensions)
	keyShareKeys := uConn.HandshakeState.State13.KeyShareKeys
	if keyShareKeys == nil || keyShareKeys.Mlkem == nil || keyShareKeys.MlkemEcdhe == nil {
		t.Fatal("hybrid X25519MLKEM768 private key was not generated")
	}
	if keyShareKeys.Ecdhe == nil {
		t.Fatal("X25519 fallback private key was not preserved")
	}
}

func TestEnsureRealityHybridKeyShareDoesNotDuplicateChrome(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	helloSpec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		t.Fatal(err)
	}
	ensureRealityHybridKeyShare(helloSpec.Extensions)
	ensureRealityHybridKeyShare(helloSpec.Extensions)
	uConn := utls.UClient(clientConn, &utls.Config{ServerName: "example.com"}, utls.HelloCustom)
	if err := uConn.ApplyPreset(&helloSpec); err != nil {
		t.Fatal(err)
	}
	assertRealityHybridBeforeX25519(t, uConn.Extensions)

	for _, extension := range uConn.Extensions {
		switch typedExtension := extension.(type) {
		case *utls.SupportedCurvesExtension:
			assertSingleHybrid(t, "supported curves", typedExtension.Curves)
		case *utls.KeyShareExtension:
			groups := make([]utls.CurveID, len(typedExtension.KeyShares))
			for index, keyShare := range typedExtension.KeyShares {
				groups[index] = keyShare.Group
			}
			assertSingleHybrid(t, "key shares", groups)
		}
	}
}

func TestRealityHybridTLS13Fingerprints(t *testing.T) {
	for _, fingerprint := range []string{
		"chrome", "firefox", "edge", "safari", "qq", "ios", "random", "randomized",
	} {
		t.Run(fingerprint, func(t *testing.T) {
			clientHelloID, err := uTLSClientHelloID(fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			helloSpec, err := utls.UTLSIdToSpec(clientHelloID)
			if err != nil {
				t.Fatal(err)
			}
			ensureRealityHybridKeyShare(helloSpec.Extensions)

			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			uConn := utls.UClient(clientConn, &utls.Config{ServerName: "example.com"}, utls.HelloCustom)
			if err := uConn.ApplyPreset(&helloSpec); err != nil {
				t.Fatal(err)
			}
			if err := uConn.BuildHandshakeState(); err != nil {
				t.Fatal(err)
			}

			assertRealityHybridBeforeX25519(t, uConn.Extensions)
			keyShareKeys := uConn.HandshakeState.State13.KeyShareKeys
			if keyShareKeys == nil || keyShareKeys.Mlkem == nil || keyShareKeys.MlkemEcdhe == nil || keyShareKeys.Ecdhe == nil {
				if keyShareKeys == nil {
					t.Fatal("REALITY key share private state was not generated")
				}
				t.Fatalf("REALITY private keys incomplete: curve=%v mlkem=%t mlkem_ecdh=%t ecdh=%t",
					keyShareKeys.CurveID, keyShareKeys.Mlkem != nil, keyShareKeys.MlkemEcdhe != nil, keyShareKeys.Ecdhe != nil)
			}
		})
	}
}

func assertSingleHybrid(t *testing.T, name string, groups []utls.CurveID) {
	t.Helper()
	count := 0
	for _, group := range groups {
		if group == utls.X25519MLKEM768 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%s must contain one X25519MLKEM768, got %d: %v", name, count, groups)
	}
}

func assertRealityHybridBeforeX25519(t *testing.T, extensions []utls.TLSExtension) {
	t.Helper()
	foundCurves := false
	foundKeyShares := false
	for _, extension := range extensions {
		switch typedExtension := extension.(type) {
		case *utls.SupportedCurvesExtension:
			foundCurves = true
			assertGroupOrder(t, "supported curves", typedExtension.Curves)
		case *utls.KeyShareExtension:
			foundKeyShares = true
			groups := make([]utls.CurveID, len(typedExtension.KeyShares))
			for index, keyShare := range typedExtension.KeyShares {
				groups[index] = keyShare.Group
			}
			assertGroupOrder(t, "key shares", groups)
		}
	}
	if !foundCurves || !foundKeyShares {
		t.Fatalf("ClientHello is missing TLS 1.3 curve extensions: curves=%t key_shares=%t", foundCurves, foundKeyShares)
	}
}

func assertGroupOrder(t *testing.T, name string, groups []utls.CurveID) {
	t.Helper()
	hybridIndex := -1
	x25519Index := -1
	for index, group := range groups {
		switch group {
		case utls.X25519MLKEM768:
			hybridIndex = index
		case utls.X25519:
			x25519Index = index
		}
	}
	if hybridIndex < 0 || x25519Index < 0 || hybridIndex >= x25519Index {
		t.Fatalf("%s must contain X25519MLKEM768 before X25519: %v", name, groups)
	}
}
