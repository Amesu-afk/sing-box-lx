//go:build with_utls

package tls

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net"
	"testing"
	"time"

	tf "github.com/sagernet/sing-box/common/tlsfragment"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 088 / SPEC 089 guards for the REALITY client.
//
// SPEC 088: `fragment` / `record_fragment` (and the SPEC 060 detour default) are
// applied to the REALITY ClientHello. Upstream builds the UConn on the bare conn,
// so the flags were written to the config and ignored — the test that catches a
// merge putting that back is TestLxRealityClientHelloGoesThroughFragmentConn.
//
// SPEC 089: `reality.key_share` — "" keeps what the fingerprint carries (the
// SPEC 083 contract), "classical" strips X25519MLKEM768 from key_share and
// supported_groups, "hybrid" fails early when the fingerprint has no hybrid share.

func lxRealityOptions(fingerprint, keyShare string, fragment, recordFragment bool) option.OutboundTLSOptions {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return option.OutboundTLSOptions{
		Enabled:        true,
		ServerName:     "www.example.com",
		Fragment:       fragment,
		RecordFragment: recordFragment,
		UTLS:           &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint},
		Reality: &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
			ShortID:   "0123abcd",
			KeyShare:  keyShare,
		},
	}
}

func lxNewRealityClient(t *testing.T, fingerprint, keyShare string, fragment, recordFragment bool) *RealityClientConfig {
	t.Helper()
	config, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", lxRealityOptions(fingerprint, keyShare, fragment, recordFragment))
	require.NoError(t, err)
	reality, isReality := config.(*RealityClientConfig)
	require.True(t, isReality, "expected *RealityClientConfig, got %T", config)
	return reality
}

// lxRealityPrepare builds the ClientHello the client would send on a pipe and
// returns the UConn for inspection — nothing is written.
func lxRealityPrepare(t *testing.T, client *RealityClientConfig) *utls.UConn {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() {
		conn.Close()
		peer.Close()
	})
	uConn, verifier, err := client.prepareClientHello(conn)
	require.NoError(t, err)
	require.NotNil(t, verifier)
	require.NotNil(t, verifier.authKey, "AuthKey must be derived before the hello is sent")
	return uConn
}

// lxTLSRecordCount splits a byte stream into TLS records and returns how many
// handshake records it holds; a broken stream yields -1.
func lxTLSRecordCount(t *testing.T, stream []byte) int {
	t.Helper()
	count := 0
	for len(stream) > 0 {
		if len(stream) < 5 {
			return -1
		}
		if stream[0] != 0x16 {
			return -1
		}
		length := int(stream[3])<<8 | int(stream[4])
		if len(stream) < 5+length {
			return -1
		}
		stream = stream[5+length:]
		count++
	}
	return count
}

// lxRealityFirstFlight runs the handshake against a pipe whose far end reads the
// first flight and hangs up, and returns the bytes that reached the wire.
func lxRealityFirstFlight(t *testing.T, client *RealityClientConfig) []byte {
	t.Helper()
	conn, peer := net.Pipe()
	firstFlight := make(chan []byte, 1)
	go func() {
		defer peer.Close()
		buffer := make([]byte, 64*1024)
		n, err := peer.Read(buffer)
		if err != nil {
			firstFlight <- nil
			return
		}
		firstFlight <- buffer[:n]
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.ClientHandshake(ctx, conn)
	require.Error(t, err, "peer hangs up after the first flight; the handshake must fail, not succeed")
	conn.Close()
	select {
	case flight := <-firstFlight:
		require.NotNil(t, flight, "peer read nothing")
		return flight
	case <-time.After(5 * time.Second):
		t.Fatal("peer never saw the first flight")
		return nil
	}
}

// --- SPEC 088 -----------------------------------------------------------------

// The REALITY client must hand the fragment wrappers the same conn the plain
// uTLS client would: with either flag set the UConn sits on a *tf.Conn, with
// neither it sits on the raw conn (no accidental wrapping cost).
func TestLxRealityClientHelloGoesThroughFragmentConn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                     string
		fragment, recordFragment bool
		wantWrapped              bool
	}{
		{"none", false, false, false},
		{"fragment", true, false, true},
		{"record_fragment", false, true, true},
		{"both", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uConn := lxRealityPrepare(t, lxNewRealityClient(t, "chrome", "", tc.fragment, tc.recordFragment))
			_, isTF := uConn.NetConn().(*tf.Conn)
			require.Equal(t, tc.wantWrapped, isTF, "UConn.NetConn() is %T", uConn.NetConn())
		})
	}
}

// End to end on the wire: with record_fragment the REALITY first flight is more
// than one TLS record (tlsfragment splits the first record around the SNI);
// without it, exactly one. Before SPEC 088 both cases sent one record.
func TestLxRealityRecordFragmentSplitsFirstFlight(t *testing.T) {
	t.Parallel()
	plain := lxRealityFirstFlight(t, lxNewRealityClient(t, "chrome", "", false, false))
	require.Equal(t, 1, lxTLSRecordCount(t, plain), "without fragmentation the ClientHello is one record")

	fragmented := lxRealityFirstFlight(t, lxNewRealityClient(t, "chrome", "", false, true))
	require.GreaterOrEqual(t, lxTLSRecordCount(t, fragmented), 2, "record_fragment must split the REALITY ClientHello into several TLS records")
}

// --- SPEC 089 -----------------------------------------------------------------

// Default (empty) key_share keeps SPEC 083: the fingerprint's hybrid share goes
// on the wire, ahead of X25519, for all three presets the fork stands on.
func TestLxRealityKeyShareDefaultKeepsHybrid(t *testing.T) {
	t.Parallel()
	for _, fingerprint := range []string{"chrome", "firefox", "safari"} {
		t.Run(fingerprint, func(t *testing.T) {
			t.Parallel()
			uConn := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareDefault, false, false))
			hello := uConn.HandshakeState.Hello
			hybridIndex, hybridCount := lxIndexOfShare(hello.KeyShares, utls.X25519MLKEM768)
			classicalIndex, _ := lxIndexOfShare(hello.KeyShares, utls.X25519)
			require.NotEqual(t, -1, hybridIndex, "hybrid share missing from the wire hello")
			require.Equal(t, 1, hybridCount)
			require.Less(t, hybridIndex, classicalIndex, "X25519MLKEM768 must precede X25519")
			require.NotEqual(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519MLKEM768))
		})
	}
}

// "classical" removes X25519MLKEM768 from both key_share and supported_groups —
// the pre-083 upstream ClientHello, ~1.2 KB smaller — and leaves the X25519
// share (with its private key, which REALITY derives AuthKey from) in place.
func TestLxRealityKeyShareClassicalStripsHybrid(t *testing.T) {
	t.Parallel()
	for _, fingerprint := range []string{"chrome", "firefox", "safari"} {
		t.Run(fingerprint, func(t *testing.T) {
			t.Parallel()
			hybrid := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareDefault, false, false))
			classical := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareClassical, false, false))
			hello := classical.HandshakeState.Hello

			hybridIndex, _ := lxIndexOfShare(hello.KeyShares, utls.X25519MLKEM768)
			require.Equal(t, -1, hybridIndex, "X25519MLKEM768 must be gone from key_share")
			classicalIndex, classicalCount := lxIndexOfShare(hello.KeyShares, utls.X25519)
			require.NotEqual(t, -1, classicalIndex, "X25519 share must stay")
			require.Equal(t, 1, classicalCount)
			require.Len(t, hello.KeyShares[classicalIndex].Data, 32)
			require.Equal(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519MLKEM768), "X25519MLKEM768 must be gone from supported_groups")
			require.NotEqual(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519))

			// Wire size, deterministically: a hello that carries the 1184-byte ML-KEM-768
			// encapsulation key cannot be shorter than it, and one without it stays under
			// (Chrome's GREASE ECH payload is random-sized, so two independently built
			// hellos are not compared against each other).
			require.GreaterOrEqual(t, len(hybrid.HandshakeState.Hello.Raw), 1184+32, "hybrid hello must carry the ML-KEM share (%d bytes)", len(hybrid.HandshakeState.Hello.Raw))
			require.Less(t, len(hello.Raw), 1184, "classical hello must be shorter than an ML-KEM-768 encapsulation key alone (%d bytes)", len(hello.Raw))

			// AuthKey source (SPEC 083 contract): the X25519 private key is still there
			// and matches the share on the wire.
			keys := classical.HandshakeState.State13.KeyShareKeys
			require.NotNil(t, keys)
			require.NotNil(t, keys.Ecdhe, "Ecdhe must remain — REALITY derives AuthKey from it")
			require.Equal(t, hello.KeyShares[classicalIndex].Data, keys.Ecdhe.PublicKey().Bytes())
		})
	}
}

// "hybrid" is a check, not an action: on a fingerprint that carries the share it
// changes nothing; on one that does not (edge = HelloEdge_85, X25519 only) the
// handshake fails at once with a config-shaped error instead of the silent
// `reality verification failed` the server would have produced.
func TestLxRealityKeyShareHybridRequiresShare(t *testing.T) {
	t.Parallel()
	uConn := lxRealityPrepare(t, lxNewRealityClient(t, "chrome", C.RealityKeyShareHybrid, false, false))
	hybridIndex, _ := lxIndexOfShare(uConn.HandshakeState.Hello.KeyShares, utls.X25519MLKEM768)
	require.NotEqual(t, -1, hybridIndex)

	edge := lxNewRealityClient(t, "edge", C.RealityKeyShareHybrid, false, false)
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	_, _, err := edge.prepareClientHello(conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "carries no X25519MLKEM768 key share")
	require.Contains(t, err.Error(), "Edge")
}

// Typos are rejected when the outbound is built, not turned into the default.
func TestLxRealityKeyShareUnknownValueRejected(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"classic", "mlkem", "Hybrid", "auto"} {
		_, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", lxRealityOptions("chrome", value, false, false))
		require.Error(t, err, value)
		require.Contains(t, err.Error(), "unknown reality key_share", value)
	}
}

// Clone() must carry the policy: outbounds clone the TLS config per dial.
func TestLxRealityKeyShareSurvivesClone(t *testing.T) {
	t.Parallel()
	client := lxNewRealityClient(t, "chrome", C.RealityKeyShareClassical, false, false)
	cloned, isReality := client.Clone().(*RealityClientConfig)
	require.True(t, isReality)
	require.Equal(t, C.RealityKeyShareClassical, cloned.keyShare)
	hybridIndex, _ := lxIndexOfShare(lxRealityPrepare(t, cloned).HandshakeState.Hello.KeyShares, utls.X25519MLKEM768)
	require.Equal(t, -1, hybridIndex)
}
