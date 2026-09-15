//go:build with_utls

package tls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	mRand "math/rand"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	tf "github.com/sagernet/sing-box/common/tlsfragment"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/debug"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/ntp"
	aTLS "github.com/sagernet/sing/common/tls"

	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/net/http2"
)

var _ ConfigCompat = (*RealityClientConfig)(nil)

type RealityClientConfig struct {
	ctx       context.Context
	uClient   *UTLSClientConfig
	publicKey []byte
	shortID   [8]byte
}

func NewRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions) (Config, error) {
	return newRealityClient(ctx, logger, serverAddress, options, false)
}

func newRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions, allowEmptyServerName bool) (Config, error) {
	if options.UTLS == nil || !options.UTLS.Enabled {
		return nil, E.New("uTLS is required by reality client")
	}
	if options.Spoof != "" || options.SpoofMethod != "" {
		return nil, E.New("spoof is unsupported in reality")
	}

	uClient, err := newUTLSClient(ctx, logger, serverAddress, options, allowEmptyServerName)
	if err != nil {
		return nil, err
	}

	publicKey, err := base64.RawURLEncoding.DecodeString(options.Reality.PublicKey)
	if err != nil {
		return nil, E.Cause(err, "decode public_key")
	}
	if len(publicKey) != 32 {
		return nil, E.New("invalid public_key")
	}
	var shortID [8]byte
	decodedLen, err := hex.Decode(shortID[:], []byte(options.Reality.ShortID))
	if err != nil {
		return nil, E.Cause(err, "decode short_id")
	}
	if decodedLen > 8 {
		return nil, E.New("invalid short_id")
	}

	var config Config = &RealityClientConfig{ctx, uClient.(*UTLSClientConfig), publicKey, shortID}
	if options.KernelRx || options.KernelTx {
		if !C.IsLinux {
			return nil, E.New("kTLS is only supported on Linux")
		}
		config = &KTLSClientConfig{
			Config:   config,
			logger:   logger,
			kernelTx: options.KernelTx,
			kernelRx: options.KernelRx,
		}
	}
	return config, nil
}

func (e *RealityClientConfig) ServerName() string {
	return e.uClient.ServerName()
}

func (e *RealityClientConfig) SetServerName(serverName string) {
	e.uClient.SetServerName(serverName)
}

func (e *RealityClientConfig) NextProtos() []string {
	return e.uClient.NextProtos()
}

func (e *RealityClientConfig) SetNextProtos(nextProto []string) {
	e.uClient.SetNextProtos(nextProto)
}

func (e *RealityClientConfig) HandshakeTimeout() time.Duration {
	return e.uClient.HandshakeTimeout()
}

func (e *RealityClientConfig) SetHandshakeTimeout(timeout time.Duration) {
	e.uClient.SetHandshakeTimeout(timeout)
}

func (e *RealityClientConfig) STDConfig() (*STDConfig, error) {
	return nil, E.New("unsupported usage for reality")
}

func (e *RealityClientConfig) Client(conn net.Conn) (Conn, error) {
	return ClientHandshake(context.Background(), conn, e)
}

func (e *RealityClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	// lx: REALITY builds its uTLS connection straight on the socket, so it never passed
	// through UTLSClientConfig.Client() where the fragmenter is installed — which meant the
	// TLS-fragmentation setting was silently inert on the one transport people actually use
	// against DPI. Install it here on the same terms.
	//
	// Safe with respect to REALITY's own checks: tlsfragment.Conn overrides Write only (reads
	// pass through the embedded conn, ReaderReplaceable reports true), and packet splitting
	// does not alter a byte of the ClientHello — only how it is spread across segments — so
	// the HMAC REALITY embeds in the session id still covers the same content.
	if e.uClient.fragment || e.uClient.recordFragment {
		conn = tf.NewConn(conn, ctx, e.uClient.fragment, e.uClient.recordFragment, e.uClient.fragmentFallbackDelay)
	}
	verifier := &realityVerifier{
		serverName: e.uClient.ServerName(),
	}
	uConfig := e.uClient.config.Clone()
	uConfig.InsecureSkipVerify = true
	uConfig.SessionTicketsDisabled = true
	uConfig.VerifyPeerCertificate = verifier.VerifyPeerCertificate
	helloSpec, err := utls.UTLSIdToSpec(e.uClient.id)
	if err != nil {
		return nil, err
	}
	// lx: SPEC 053 — Xray v26.9.8+ requires the X25519MLKEM768 key share
	// before the optional X25519 fallback. Older sing-box removed the hybrid
	// share for REALITY, and Firefox fingerprints do not add it themselves.
	ensureRealityHybridKeyShare(helloSpec.Extensions)
	uConn := utls.UClient(conn, uConfig, utls.HelloCustom)
	verifier.UConn = uConn
	err = uConn.ApplyPreset(&helloSpec)
	if err != nil {
		return nil, err
	}
	err = uConn.BuildHandshakeState()
	if err != nil {
		return nil, err
	}

	if len(uConfig.NextProtos) > 0 {
		for _, extension := range uConn.Extensions {
			if alpnExtension, isALPN := extension.(*utls.ALPNExtension); isALPN {
				alpnExtension.AlpnProtocols = uConfig.NextProtos
				break
			}
		}
	}

	hello := uConn.HandshakeState.Hello
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:], hello.SessionId)

	var nowTime time.Time
	if uConfig.Time != nil {
		nowTime = uConfig.Time()
	} else {
		nowTime = time.Now()
	}
	binary.BigEndian.PutUint64(hello.SessionId, uint64(nowTime.Unix()))

	// lx: SPEC 053 — Xray v26.7.11 (XTLS/Xray-core@af7eb68) включил
	// minClientVer=26.3.27 по умолчанию. Апстримный `1, 8, 1` ниже порога:
	// сервер не отдаёт ошибку, а молча проксирует на камуфляжный dest.
	// Объявляем ровно минимум — сравнение там `>=`, выше не нужно.
	hello.SessionId[0] = 26
	hello.SessionId[1] = 3
	hello.SessionId[2] = 27
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(time.Now().Unix()))
	copy(hello.SessionId[8:], e.shortID[:])
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId[:16]: %v\n", hello.SessionId[:16])
	}
	publicKey, err := ecdh.X25519().NewPublicKey(e.publicKey)
	if err != nil {
		return nil, err
	}
	keyShareKeys := uConn.HandshakeState.State13.KeyShareKeys
	if keyShareKeys == nil {
		return nil, E.New("nil KeyShareKeys")
	}
	ecdheKey := keyShareKeys.Ecdhe
	if ecdheKey == nil {
		return nil, E.New("nil ecdheKey")
	}
	authKey, err := ecdheKey.ECDH(publicKey)
	if err != nil {
		return nil, err
	}
	if authKey == nil {
		return nil, E.New("nil auth_key")
	}
	verifier.authKey = authKey
	_, err = hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey)
	if err != nil {
		return nil, err
	}
	aesBlock, _ := aes.NewCipher(authKey)
	aesGcmCipher, _ := cipher.NewGCM(aesBlock)
	aesGcmCipher.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId: %v\n", hello.SessionId)
		fmt.Printf("REALITY uConn.AuthKey: %v\n", authKey)
	}

	err = uConn.HandshakeContext(ctx)
	if err != nil {
		return nil, err
	}

	if debug.Enabled {
		fmt.Printf("REALITY Conn.Verified: %v\n", verifier.verified)
	}

	if !verifier.verified {
		go realityClientFallback(e.ctx, uConn, e.uClient.ServerName(), e.uClient.id)
		return nil, E.New("reality verification failed")
	}

	return &realityClientConnWrapper{uConn}, nil
}

// ensureRealityHybridKeyShare keeps GREASE ordering intact, inserts the hybrid
// group immediately before X25519, and preserves X25519 for REALITY's auth key.
func ensureRealityHybridKeyShare(extensions []utls.TLSExtension) {
	for _, extension := range extensions {
		switch typedExtension := extension.(type) {
		case *utls.SupportedCurvesExtension:
			insertAt := -1
			curves := make([]utls.CurveID, 0, len(typedExtension.Curves)+2)
			for _, curveID := range typedExtension.Curves {
				if curveID == utls.X25519MLKEM768 || curveID == utls.X25519 {
					if insertAt < 0 {
						insertAt = len(curves)
					}
					continue
				}
				curves = append(curves, curveID)
			}
			if insertAt < 0 {
				insertAt = len(curves)
			}
			curves = append(curves, 0, 0)
			copy(curves[insertAt+2:], curves[insertAt:len(curves)-2])
			curves[insertAt] = utls.X25519MLKEM768
			curves[insertAt+1] = utls.X25519
			typedExtension.Curves = curves
		case *utls.KeyShareExtension:
			insertAt := -1
			var hybridShare utls.KeyShare
			var x25519Share utls.KeyShare
			keyShares := make([]utls.KeyShare, 0, len(typedExtension.KeyShares)+2)
			for _, keyShare := range typedExtension.KeyShares {
				if keyShare.Group == utls.X25519MLKEM768 || keyShare.Group == utls.X25519 {
					if insertAt < 0 {
						insertAt = len(keyShares)
					}
					if keyShare.Group == utls.X25519MLKEM768 {
						hybridShare = keyShare
					} else {
						x25519Share = keyShare
					}
					continue
				}
				keyShares = append(keyShares, keyShare)
			}
			if insertAt < 0 {
				insertAt = len(keyShares)
			}
			hybridShare.Group = utls.X25519MLKEM768
			x25519Share.Group = utls.X25519
			keyShares = append(keyShares, utls.KeyShare{}, utls.KeyShare{})
			copy(keyShares[insertAt+2:], keyShares[insertAt:len(keyShares)-2])
			keyShares[insertAt] = hybridShare
			keyShares[insertAt+1] = x25519Share
			typedExtension.KeyShares = keyShares
		}
	}
}

func realityClientFallback(ctx context.Context, uConn net.Conn, serverName string, fingerprint utls.ClientHelloID) {
	defer uConn.Close()
	client := &http.Client{
		Transport: &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, config *tls.Config) (net.Conn, error) {
				return uConn, nil
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(ctx),
				RootCAs: adapter.RootPoolFromContext(ctx),
			},
		},
	}
	request, _ := http.NewRequest("GET", "https://"+serverName, nil)
	request.Header.Set("User-Agent", fingerprint.Client)
	request.AddCookie(&http.Cookie{Name: "padding", Value: strings.Repeat("0", mRand.Intn(32)+30)})
	response, err := client.Do(request)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
}

func (e *RealityClientConfig) Clone() Config {
	return &RealityClientConfig{
		e.ctx,
		e.uClient.Clone().(*UTLSClientConfig),
		e.publicKey,
		e.shortID,
	}
}

type realityVerifier struct {
	*utls.UConn
	serverName string
	authKey    []byte
	verified   bool
}

func (c *realityVerifier) VerifyPeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	p, _ := reflect.TypeFor[utls.Conn]().FieldByName("peerCertificates")
	certs := *(*([]*x509.Certificate))(unsafe.Add(unsafe.Pointer(c.Conn), p.Offset))
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKey)
		h.Write(pub)
		if bytes.Equal(h.Sum(nil), certs[0].Signature) {
			c.verified = true
			return nil
		}
	}
	opts := x509.VerifyOptions{
		DNSName:       c.serverName,
		Intermediates: x509.NewCertPool(),
	}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	return nil
}

type realityClientConnWrapper struct {
	*utls.UConn
}

func (c *realityClientConnWrapper) ConnectionState() tls.ConnectionState {
	state := c.Conn.ConnectionState()
	//nolint:staticcheck
	return tls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            state.PeerCertificates,
		VerifiedChains:              state.VerifiedChains,
		SignedCertificateTimestamps: state.SignedCertificateTimestamps,
		OCSPResponse:                state.OCSPResponse,
		TLSUnique:                   state.TLSUnique,
	}
}

func (c *realityClientConnWrapper) Upstream() any {
	return c.UConn
}

// Due to low implementation quality, the reality server intercepted half close and caused memory leaks.
// We fixed it by calling Close() directly.
func (c *realityClientConnWrapper) CloseWrite() error {
	return c.Close()
}

func (c *realityClientConnWrapper) ReaderReplaceable() bool {
	return true
}

func (c *realityClientConnWrapper) WriterReplaceable() bool {
	return true
}
