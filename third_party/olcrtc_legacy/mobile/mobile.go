// Package mobile provides the small gomobile surface used by TarnVPN for
// manager-generated olcRTC profiles. It intentionally avoids the manager's
// desktop/session package so the Android binding does not pull subscription
// storage or the unused server/livekit code into the AAR.
package mobile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/openlibrecommunity/olcrtc/internal/client"
	"github.com/openlibrecommunity/olcrtc/internal/control"
	enginebuiltin "github.com/openlibrecommunity/olcrtc/internal/engine/builtin"
	"github.com/openlibrecommunity/olcrtc/internal/logger"
	"github.com/openlibrecommunity/olcrtc/internal/protect"
	"github.com/openlibrecommunity/olcrtc/internal/transport"
	"github.com/openlibrecommunity/olcrtc/internal/transport/datachannel"
	"github.com/openlibrecommunity/olcrtc/internal/transport/vp8channel"

	_ "golang.org/x/mobile/bind"
)

// SocketProtector protects a socket from Android's VPN routing loop.
type SocketProtector interface {
	Protect(fd int) bool
}

// LogWriter receives log messages from olcRTC.
type LogWriter interface {
	WriteLog(msg string)
}

// logBridge adapts LogWriter to io.Writer.
type logBridge struct {
	w LogWriter
}

func (b *logBridge) Write(p []byte) (int, error) {
	b.w.WriteLog(string(p))
	return len(p), nil
}

const (
	defaultTransport = "vp8channel"
	dataTransport    = "datachannel"
	defaultDNS       = "8.8.8.8:53"
)

var (
	mu       sync.Mutex
	defaults = mobileConfig{
		transport:        defaultTransport,
		dnsServer:        defaultDNS,
		vp8FPS:           60,
		vp8BatchSize:     8,
		livenessInterval: control.DefaultInterval,
		livenessTimeout:  control.DefaultTimeout,
		livenessFailures: control.DefaultFailures,
	}
	registered        bool
	cancel            context.CancelFunc
	done              chan struct{}
	ready             chan struct{}
	errRun            error
	controlRTTMillis  = -1
	controlLastPong   time.Time
	controlSessionID  string
	controlReconnects uint64
)

type mobileConfig struct {
	transport        string
	dnsServer        string
	vp8FPS           int
	vp8BatchSize     int
	livenessInterval time.Duration
	livenessTimeout  time.Duration
	livenessFailures int
}

// SetProtector sets the Android VPN socket protector. Call it before Start.
func SetProtector(p SocketProtector) {
	if p == nil {
		protect.Protector = nil
		return
	}
	protect.Protector = func(fd int) bool { return p.Protect(fd) }
}

// SetLogWriter routes olcRTC log output to the caller. Without it the
// runtime logs into the process default, which is invisible on Android.
func SetLogWriter(w LogWriter) {
	if w != nil {
		log.SetOutput(&logBridge{w: w})
	}
}

// SetDebug enables verbose olcRTC logging, including the carrier handshake
// diagnostics needed to tell a missing peer from a stalled bridge.
func SetDebug(enabled bool) {
	logger.SetVerbose(enabled)
	if enabled {
		log.SetFlags(log.Ltime | log.Lshortfile)
		return
	}
	log.SetFlags(log.Ltime)
}

// SetProviders registers the legacy manager's Jitsi and transport providers.
func SetProviders() {
	mu.Lock()
	defer mu.Unlock()
	if registered {
		return
	}
	enginebuiltin.RegisterDefaults()
	transport.Register(dataTransport, datachannel.New)
	transport.Register(defaultTransport, vp8channel.New)
	registered = true
}

// SetTransport selects datachannel or vp8channel for the next start.
func SetTransport(value string) error {
	normalized := normalizeTransport(value)
	if normalized == "" {
		return errors.New("unsupported transport: want vp8channel or datachannel")
	}
	mu.Lock()
	defaults.transport = normalized
	mu.Unlock()
	return nil
}

// SetDNS selects the DNS server used by the tunnel.
func SetDNS(value string) {
	mu.Lock()
	defaults.dnsServer = value
	mu.Unlock()
}

// SetVP8Options configures the legacy vp8channel transport.
func SetVP8Options(fps, batchSize int) {
	mu.Lock()
	defaults.vp8FPS = clamp(fps, 120)
	defaults.vp8BatchSize = clamp(batchSize, 64)
	mu.Unlock()
}

// SetLivenessOptions configures control-stream checks in milliseconds.
func SetLivenessOptions(intervalMillis, timeoutMillis, failures int) {
	mu.Lock()
	if intervalMillis > 0 {
		defaults.livenessInterval = time.Duration(intervalMillis) * time.Millisecond
	}
	if timeoutMillis > 0 {
		defaults.livenessTimeout = time.Duration(timeoutMillis) * time.Millisecond
	}
	if failures > 0 {
		defaults.livenessFailures = failures
	}
	mu.Unlock()
}

// StartWithTransport launches an olcRTC client in the background. Manager QR profiles provide
// clientID explicitly; original v1 links do not, so an empty value uses the client's generated
// DeviceID fallback.
func StartWithTransport(provider, transportName, room, clientID, key string, socksPort int, socksUser, socksPass string) error {
	SetProviders()
	transportName = normalizeTransport(transportName)
	if transportName == "" {
		return errors.New("unsupported transport: want vp8channel or datachannel")
	}
	if err := validateStartArguments(provider, room, key); err != nil {
		return err
	}

	mu.Lock()
	if cancel != nil {
		mu.Unlock()
		return errors.New("olcRTC already running")
	}
	cfg := defaults
	cfg.transport = transportName
	ctx, cancelFunc := context.WithCancel(context.Background())
	cancel = cancelFunc
	done = make(chan struct{})
	ready = make(chan struct{})
	localDone, localReady := done, ready
	errRun = nil
	controlRTTMillis = -1
	controlLastPong = time.Time{}
	controlSessionID = ""
	controlReconnects = 0
	mu.Unlock()

	go func() {
		err := client.RunWithReady(ctx, client.Config{
			Transport: transportName,
			Carrier:   provider,
			RoomURL:   room,
			KeyHex:    key,
			DeviceID:  clientID,
			LocalAddr: fmt.Sprintf("127.0.0.1:%d", socksPort),
			DNSServer: cfg.dnsServer,
			SOCKSUser: socksUser,
			SOCKSPass: socksPass,
			TransportOptions: vp8channel.Options{
				FPS:       cfg.vp8FPS,
				BatchSize: cfg.vp8BatchSize,
			},
			Liveness: control.Config{
				Interval: cfg.livenessInterval,
				Timeout:  cfg.livenessTimeout,
				Failures: cfg.livenessFailures,
			},
			OnHealth: recordControlHealth,
		}, func() {
			mu.Lock()
			select {
			case <-localReady:
			default:
				close(localReady)
			}
			mu.Unlock()
		})
		mu.Lock()
		errRun = err
		cancel = nil
		controlRTTMillis = -1
		controlLastPong = time.Time{}
		mu.Unlock()
		close(localDone)
	}()
	return nil
}

// WaitReady waits for the SOCKS listener or the bounded timeout.
func WaitReady(timeoutMillis int) error {
	mu.Lock()
	r, d, runErr, running := ready, done, errRun, cancel != nil
	mu.Unlock()
	if r == nil {
		if runErr != nil {
			return runErr
		}
		return errors.New("olcRTC is not running")
	}
	select {
	case <-r:
		return nil
	default:
	}
	if !running {
		if runErr != nil {
			return runErr
		}
		return errors.New("olcRTC stopped before becoming ready")
	}
	if timeoutMillis <= 0 {
		timeoutMillis = 8_000
	}
	timer := time.NewTimer(time.Duration(timeoutMillis) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r:
		return nil
	case <-d:
		mu.Lock()
		runErr := errRun
		mu.Unlock()
		if runErr != nil {
			return runErr
		}
		return errors.New("olcRTC stopped before becoming ready")
	case <-timer.C:
		return errors.New("olcRTC start timed out")
	}
}

// Stop cancels the current client and waits for its goroutine.
func Stop() {
	mu.Lock()
	c, d := cancel, done
	mu.Unlock()
	if c == nil {
		return
	}
	c()
	if d != nil {
		<-d
	}
}

// IsRunning reports whether the singleton client is active.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return cancel != nil
}

// GetControlRTTMillis returns the last fresh encrypted control-stream round trip.
// -1 means that no active session has produced a recent pong. It does not dial
// another endpoint or restart the WebRTC carrier.
func GetControlRTTMillis() int {
	mu.Lock()
	defer mu.Unlock()
	if cancel == nil || controlRTTMillis < 0 || controlLastPong.IsZero() ||
		time.Since(controlLastPong) > 2*defaults.livenessInterval+defaults.livenessTimeout {
		return -1
	}
	return controlRTTMillis
}

func recordControlHealth(status control.Status) {
	mu.Lock()
	defer mu.Unlock()
	if cancel == nil {
		return
	}
	if status.SessionID != controlSessionID || status.Reconnects != controlReconnects {
		controlSessionID = status.SessionID
		controlReconnects = status.Reconnects
		controlRTTMillis = -1
		controlLastPong = time.Time{}
		return
	}
	if !status.LastPong.IsZero() && status.LastPong.After(controlLastPong) && status.MissedPongs == 0 {
		controlLastPong = status.LastPong
		controlRTTMillis = max(1, int(status.LastRTT/time.Millisecond))
	}
}

func validateStartArguments(provider, room, key string) error {
	if provider == "" || room == "" || key == "" {
		return errors.New("carrier, room and key are required")
	}
	return nil
}

func normalizeTransport(value string) string {
	switch value {
	case dataTransport, "data", "dc":
		return dataTransport
	case defaultTransport, "vp8":
		return defaultTransport
	default:
		return ""
	}
}

func clamp(value, max int) int {
	if value < 1 {
		return 1
	}
	if value > max {
		return max
	}
	return value
}
