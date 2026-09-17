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
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 088 guards for the REALITY client.
//
// SPEC 088: `fragment` / `record_fragment` (and the SPEC 060 detour default) are
// applied to the REALITY ClientHello. Upstream builds the UConn on the bare conn,
// so the flags were written to the config and ignored — the test that catches a
// merge putting that back is TestLxRealityClientHelloGoesThroughFragmentConn.

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
