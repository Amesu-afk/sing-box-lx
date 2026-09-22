// lx: SPEC 085 — SOCKS5 UDP ASSOCIATE с BND.ADDR 0.0.0.0/::.
//
// Red/green-дискриминатор — записывающий dialer: он показывает, КУДА клиент
// диалит UDP-релей, независимо от ОС (на macOS и Linux dial на 0.0.0.0:port
// уходит на loopback, и живой релей на 127.0.0.1 получил бы датаграмму даже на
// старом коде). Живой round-trip через настоящий NewOutbound и loopback-релей
// подтверждает провод целиком: хендшейк → релей → ответ.
//
// Два теста держат контракт с sing (см. шапку udp_associate_lx.go):
//   - TestLxUpstreamSocksClientDialsUnspecifiedBind — страж условия снятия: sing
//     ещё диалит сырой BND.ADDR; упадёт, когда апстрим начнёт нормализовать сам;
//   - TestLxRelayDialerNormalisesBind — dial релея всё ещё идёт через переданный
//     dialer; упадёт, если sing начнёт диалить релей мимо него.

package socks

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/varbin"
	"github.com/sagernet/sing/protocol/socks"
	"github.com/sagernet/sing/protocol/socks/socks5"
)

// serveFakeSocks5 — минимальный сервер: без аутентификации, на UDP ASSOCIATE
// отвечает заданным Bind и держит управляющее соединение до закрытия пиром.
// Работает в горутине, поэтому только t.Error.
func serveFakeSocks5(t *testing.T, conn net.Conn, bind M.Socksaddr) {
	defer conn.Close()
	reader := varbin.StubReader(conn)
	if _, err := socks5.ReadAuthRequest(reader); err != nil {
		t.Error("fake server: read auth request: ", err)
		return
	}
	if err := socks5.WriteAuthResponse(conn, socks5.AuthResponse{Method: socks5.AuthTypeNotRequired}); err != nil {
		t.Error("fake server: write auth response: ", err)
		return
	}
	request, err := socks5.ReadRequest(reader)
	if err != nil {
		t.Error("fake server: read request: ", err)
		return
	}
	if request.Command != socks5.CommandUDPAssociate {
		t.Errorf("fake server: expected UDP ASSOCIATE (0x03), got command 0x%02x", request.Command)
		return
	}
	if err := socks5.WriteResponse(conn, socks5.Response{ReplyCode: socks5.ReplyCodeSuccess, Bind: bind}); err != nil {
		t.Error("fake server: write response: ", err)
		return
	}
	var hold [1]byte
	_, _ = conn.Read(hold[:])
}

type recordedDial struct {
	network     string
	destination M.Socksaddr
}

// recordingDialer — N.Dialer, который обслуживает хендшейк через net.Pipe и
// записывает каждый dial (сеть + адрес).
type recordingDialer struct {
	t    *testing.T
	bind M.Socksaddr

	access sync.Mutex
	dials  []recordedDial
}

func (d *recordingDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.access.Lock()
	d.dials = append(d.dials, recordedDial{N.NetworkName(network), destination})
	d.access.Unlock()
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		client, server := net.Pipe()
		go serveFakeSocks5(d.t, server, d.bind)
		return client, nil
	case N.NetworkUDP:
		client, server := net.Pipe()
		d.t.Cleanup(func() { server.Close() })
		return client, nil
	}
	return nil, E.New("recordingDialer: unexpected network ", network)
}

func (d *recordingDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

func (d *recordingDialer) recorded(network string) []M.Socksaddr {
	d.access.Lock()
	defer d.access.Unlock()
	var result []M.Socksaddr
	for _, dial := range d.dials {
		if dial.network == network {
			result = append(result, dial.destination)
		}
	}
	return result
}

var (
	testServerV4     = M.ParseSocksaddr("203.0.113.5:1080")
	testServerDomain = M.ParseSocksaddr("proxy.example:1080")
	testTarget       = M.ParseSocksaddr("198.51.100.53:53")
)

func TestLxRelayAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bind    M.Socksaddr
		server  M.Socksaddr
		want    M.Socksaddr
		wantErr bool
	}{
		{"v4 unspecified → server ip", M.SocksaddrFrom(netip.IPv4Unspecified(), 2000), testServerV4, M.ParseSocksaddr("203.0.113.5:2000"), false},
		{"v6 unspecified → server ip", M.SocksaddrFrom(netip.IPv6Unspecified(), 2000), testServerV4, M.ParseSocksaddr("203.0.113.5:2000"), false},
		{"v4 unspecified → server domain", M.SocksaddrFrom(netip.IPv4Unspecified(), 2000), testServerDomain, M.ParseSocksaddr("proxy.example:2000"), false},
		{"empty bind → server ip", M.Socksaddr{Port: 2000}, testServerV4, M.ParseSocksaddr("203.0.113.5:2000"), false},
		{"concrete ip kept", M.ParseSocksaddr("198.51.100.7:2000"), testServerV4, M.ParseSocksaddr("198.51.100.7:2000"), false},
		{"domain bind kept", M.ParseSocksaddr("relay.example:2000"), testServerV4, M.ParseSocksaddr("relay.example:2000"), false},
		{"port 0 is an error", M.SocksaddrFrom(netip.IPv4Unspecified(), 0), testServerV4, M.Socksaddr{}, true},
		{"port 0 with concrete ip is an error", M.ParseSocksaddr("198.51.100.7:0"), testServerV4, M.Socksaddr{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relayAddress(tc.bind, tc.server)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("relayAddress(%s, %s) = %s, want %s", tc.bind, tc.server, got, tc.want)
			}
		})
	}
}

// TestLxUpstreamSocksClientDialsUnspecifiedBind — страж условия снятия SPEC 085:
// socks.Client из sing диалит BND.ADDR из ответа как есть через переданный
// dialer. Если этот тест упал — апстрим починил нормализацию сам, и шов
// socks-udp-bind в outbound.go вместе с udp_associate_lx.go надо снять.
func TestLxUpstreamSocksClientDialsUnspecifiedBind(t *testing.T) {
	dialer := &recordingDialer{t: t, bind: M.SocksaddrFrom(netip.IPv4Unspecified(), 2000)}
	client := socks.NewClient(dialer, testServerV4, socks.Version5, "", "")
	conn, err := client.ListenPacket(context.Background(), testTarget)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	dials := dialer.recorded(N.NetworkUDP)
	if len(dials) != 1 {
		t.Fatalf("expected exactly one UDP dial through the dialer, got %v", dials)
	}
	if want := M.SocksaddrFrom(netip.IPv4Unspecified(), 2000); dials[0] != want {
		t.Fatalf("sing's socks.Client dialed the relay at %s instead of the raw BND.ADDR %s: "+
			"upstream now normalises BND.ADDR itself — remove the SPEC 085 seam (outbound.go socks-udp-bind, udp_associate_lx.go)", dials[0], want)
	}
}

// TestLxRelayDialerNormalisesBind — socks.Client поверх relayDialer: релей
// диалится по адресу сервера с портом из ответа, TCP-dial к серверу не
// трогается, результат — *socks.AssociatePacketConn.
func TestLxRelayDialerNormalisesBind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server M.Socksaddr
		bind   M.Socksaddr
		want   M.Socksaddr
	}{
		{"v4 unspecified, ip server", testServerV4, M.SocksaddrFrom(netip.IPv4Unspecified(), 2000), M.ParseSocksaddr("203.0.113.5:2000")},
		{"v6 unspecified, ip server", testServerV4, M.SocksaddrFrom(netip.IPv6Unspecified(), 2000), M.ParseSocksaddr("203.0.113.5:2000")},
		{"v4 unspecified, domain server", testServerDomain, M.SocksaddrFrom(netip.IPv4Unspecified(), 2000), M.ParseSocksaddr("proxy.example:2000")},
		{"concrete bind kept", testServerV4, M.ParseSocksaddr("198.51.100.7:2000"), M.ParseSocksaddr("198.51.100.7:2000")},
		{"domain bind kept", testServerV4, M.ParseSocksaddr("relay.example:2000"), M.ParseSocksaddr("relay.example:2000")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialer := &recordingDialer{t: t, bind: tc.bind}
			client := socks.NewClient(newRelayDialer(dialer, tc.server), tc.server, socks.Version5, "", "")
			conn, err := client.ListenPacket(context.Background(), testTarget)
			if err != nil {
				t.Fatal(err)
			}
			if _, isAssociate := conn.(*socks.AssociatePacketConn); !isAssociate {
				t.Fatalf("expected *socks.AssociatePacketConn, got %T", conn)
			}
			conn.Close()
			if tcp := dialer.recorded(N.NetworkTCP); len(tcp) != 1 || tcp[0] != tc.server {
				t.Fatalf("control connection dialed at %v, want [%s]", tcp, tc.server)
			}
			udp := dialer.recorded(N.NetworkUDP)
			if len(udp) != 1 {
				t.Fatalf("expected exactly one UDP dial through the dialer (sing no longer dials the relay through it?), got %v", udp)
			}
			if udp[0] != tc.want {
				t.Fatalf("relay dialed at %s, want %s", udp[0], tc.want)
			}
		})
	}
}

func TestLxRelayDialerRejectsRelayPortZero(t *testing.T) {
	dialer := &recordingDialer{t: t, bind: M.SocksaddrFrom(netip.IPv4Unspecified(), 0)}
	client := socks.NewClient(newRelayDialer(dialer, testServerV4), testServerV4, socks.Version5, "", "")
	conn, err := client.ListenPacket(context.Background(), testTarget)
	if err == nil {
		conn.Close()
		t.Fatal("expected an error for relay port 0, got a connection")
	}
	if !strings.Contains(err.Error(), "relay port 0") {
		t.Fatalf("unexpected error: %v", err)
	}
	if udp := dialer.recorded(N.NetworkUDP); len(udp) != 0 {
		t.Fatalf("no UDP dial expected for relay port 0, got %v", udp)
	}
}

// udpEchoRelay — фейковый релей: разбирает SOCKS5-UDP-заголовок, отвечает тем
// же заголовком и "pong:"+payload на адрес отправителя.
func udpEchoRelay(t *testing.T, relay net.PacketConn) {
	buffer := make([]byte, 65535)
	for {
		n, source, err := relay.ReadFrom(buffer)
		if err != nil {
			return
		}
		if n < 3 {
			t.Error("relay: short datagram")
			continue
		}
		reader := bytes.NewReader(buffer[3:n])
		destination, err := M.SocksaddrSerializer.ReadAddrPort(reader)
		if err != nil {
			t.Error("relay: bad header: ", err)
			continue
		}
		payload := buffer[n-reader.Len() : n]
		var reply bytes.Buffer
		reply.Write([]byte{0, 0, 0})
		_ = M.SocksaddrSerializer.WriteAddrPort(&reply, destination)
		reply.WriteString("pong:")
		reply.Write(payload)
		if _, err := relay.WriteTo(reply.Bytes(), source); err != nil {
			t.Error("relay: write: ", err)
		}
	}
}

// TestLxNewOutboundUDPThroughUnspecifiedBind — шов socks-udp-bind в NewOutbound
// на живом проводе: настоящий outbound, фейковый socks5-сервер на 127.0.0.1
// отвечает BND.ADDR [::]:relayPort (без фикса dial "[::]:port" уходит на [::1]
// и релей на 127.0.0.1 датаграмму не видит), 0.0.0.0:relayPort и своим
// настоящим адресом; датаграмма доходит до релея и ответ возвращается с
// адресом цели. Для version 4 обёртка не ставится — UDP там по-прежнему
// отвергается самим sing.
func TestLxNewOutboundUDPThroughUnspecifiedBind(t *testing.T) {
	relay, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no IPv4 loopback: ", err)
	}
	defer relay.Close()
	go udpEchoRelay(t, relay)
	relayPort := uint16(relay.LocalAddr().(*net.UDPAddr).Port)

	newOutboundTo := func(t *testing.T, listener net.Listener, version string) adapter.Outbound {
		serverAddr := M.ParseSocksaddr(listener.Addr().String())
		out, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().NewLogger("socks"), "socks-out", option.SOCKSOutboundOptions{
			ServerOptions: option.ServerOptions{Server: serverAddr.AddrString(), ServerPort: serverAddr.Port},
			Version:       version,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	serve := func(t *testing.T, bind M.Socksaddr) net.Listener {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go serveFakeSocks5(t, conn, bind)
			}
		}()
		return listener
	}

	for _, tc := range []struct {
		name string
		bind M.Socksaddr
	}{
		{"bind [::]:port", M.SocksaddrFrom(netip.IPv6Unspecified(), relayPort)},
		{"bind 0.0.0.0:port", M.SocksaddrFrom(netip.IPv4Unspecified(), relayPort)},
		{"bind 127.0.0.1:port (unchanged path)", M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), relayPort)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener := serve(t, tc.bind)
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			packetConn, err := newOutboundTo(t, listener, "5").ListenPacket(ctx, testTarget)
			if err != nil {
				t.Fatal(err)
			}
			defer packetConn.Close()

			if _, err := packetConn.WriteTo([]byte("ping"), testTarget.UDPAddr()); err != nil {
				t.Fatal(err)
			}
			_ = packetConn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buffer := make([]byte, 64)
			n, from, err := packetConn.ReadFrom(buffer)
			if err != nil {
				t.Fatalf("no reply through the relay: %v", err)
			}
			if got := string(buffer[:n]); got != "pong:ping" {
				t.Fatalf("reply payload %q, want %q", got, "pong:ping")
			}
			if got := M.SocksaddrFromNet(from); got != testTarget {
				t.Fatalf("reply source %s, want %s", got, testTarget)
			}
		})
	}

	t.Run("version 4 keeps sing's udp unsupported error", func(t *testing.T) {
		listener := serve(t, M.SocksaddrFrom(netip.IPv6Unspecified(), relayPort))
		defer listener.Close()
		packetConn, err := newOutboundTo(t, listener, "4").ListenPacket(context.Background(), testTarget)
		if err == nil {
			packetConn.Close()
			t.Fatal("expected socks4 udp unsupported error")
		}
		if !strings.Contains(err.Error(), "udp unsupported") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
