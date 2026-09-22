// lx: SPEC 085 — SOCKS5 UDP ASSOCIATE с неопределённым BND.ADDR (0.0.0.0 / ::).
//
// sing v0.9.3 (protocol/socks/client.go:162) диалит UDP-релей ровно по адресу
// из ответа сервера: c.dialer.DialContext(ctx, "udp", response.Bind). Многие
// серверы (публичные прокси, всё за NAT) отвечают 0.0.0.0:port или [::]:port —
// «тот же хост, что и управляющее соединение». Для Go неопределённый хост
// означает локальную систему (документация net.Dial), поэтому каждая
// датаграмма уходила на 127.0.0.1: TCP через такой прокси ходил, UDP — нет.
// Xray в этом случае подставляет адрес прокси (proxy/socks/client.go:97-100).
//
// Конструкция: НЕ копия клиента sing, а обёртка над dialer'ом, который sing
// получает в socks.NewClient. Хендшейк, отмена по ctx, таймауты — всё остаётся
// в sing и меняется вместе с ним; обёртка перехватывает только UDP-dial
// (единственный UDP-dial через этот dialer — релей ассоциации) и нормализует
// адрес через relayAddress. TCP-dial'ы (CONNECT, BIND, UoT) проходят как есть.
//
// За чем следить на бампе sing (контракт, на котором держится фикс): релей
// по-прежнему диалится через переданный dialer с сырым BND.ADDR. Это
// фиксируют TestLxUpstreamSocksClientDialsUnspecifiedBind (страж: sing ещё не
// нормализует сам — иначе фикс снимается) и TestLxRelayDialerNormalisesBind
// (dial всё ещё идёт через наш dialer — иначе фикс перестал действовать).

package socks

import (
	"context"
	"net"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// relayDialer — N.Dialer для socks.Client: UDP-dial релея нормализуется,
// остальное передаётся нижележащему dialer'у без изменений.
type relayDialer struct {
	N.Dialer
	serverAddr M.Socksaddr
}

func newRelayDialer(dialer N.Dialer, serverAddr M.Socksaddr) N.Dialer {
	return &relayDialer{Dialer: dialer, serverAddr: serverAddr}
}

func (d *relayDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) == N.NetworkUDP {
		relay, err := relayAddress(destination, d.serverAddr)
		if err != nil {
			return nil, err
		}
		destination = relay
	}
	return d.Dialer.DialContext(ctx, network, destination)
}

func (d *relayDialer) Upstream() any {
	return d.Dialer
}

// relayAddress — адрес, куда слать датаграммы ассоциации.
//
// Полноценный BND.ADDR (конкретный IP или домен) берётся как есть.
// Неопределённый (0.0.0.0, ::) или пустой означает «сам прокси-сервер»:
// берётся адрес сервера из конфига с портом из ответа. Именно конфиг, а не
// RemoteAddr() управляющего соединения: под detour удалённый адрес — это пир
// detour-плеча (сервер vless/socks/http), а не SOCKS-сервер; домен из конфига
// резолвит тот же dialer, что поднял управляющее соединение, а detour-плечо
// получает его как обычный UDP-dial по домену. Xray делает так же
// (dest.Address).
//
// Порт 0 в ответе использовать нельзя — это ошибка хендшейка, а не адрес.
func relayAddress(bind M.Socksaddr, serverAddr M.Socksaddr) (M.Socksaddr, error) {
	if bind.Port == 0 {
		return M.Socksaddr{}, E.New("socks5: udp associate: server replied with relay port 0 (bind ", bind.String(), ")")
	}
	if bind.IsDomain() || (bind.IsIP() && !bind.Addr.IsUnspecified()) {
		return bind, nil
	}
	relay := serverAddr
	relay.Port = bind.Port
	return relay, nil
}
