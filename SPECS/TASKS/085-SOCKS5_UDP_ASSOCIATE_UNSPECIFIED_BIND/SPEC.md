# SPEC: 085 — SOCKS5_UDP_ASSOCIATE_UNSPECIFIED_BIND

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — дефект библиотеки sing (апстрим): клиент SOCKS5 после UDP ASSOCIATE диалит релей по BND.ADDR из ответа как есть; при `0.0.0.0`/`::` датаграммы уходят на локальную систему |
| Статус | I (implemented) — red/green на записывающем dialer, страж на апстримный `socks.Client`, контракт «релей диалится через переданный dialer», живой round-trip через настоящий `NewOutbound`, `-race`; **полевая проверка на прокси репортёра открыта** |
| Связанные | отчёт Cultsonfire (Telegram, 2026-09-14, LxBox 2.23.1, ядро `lx.34`), передан сессией LxBox; sing `protocol/socks/client.go:161-167`; Xray `proxy/socks/client.go:97-100` |

Пользователь с socks5-узлами (публичные прокси, часть через `detour` на MASQUE)
получает рабочий TCP и мёртвый UDP; через v2rayNG (Xray) те же прокси по UDP
работают. Без MASQUE картина та же — `detour` не при чём. Корень — в клиенте
sing: после UDP ASSOCIATE он диалит UDP-релей ровно по адресу из ответа
сервера, а серверы, отвечающие `0.0.0.0:port`/`[::]:port` («тот же хост»),
получают ничего: для Go неопределённый хост означает локальную систему, и
каждая датаграмма уходит на `127.0.0.1`. Xray в этом случае подставляет адрес
прокси. Фикс — та же подстановка, но в нашем слое: обёртка dialer'а, которую
sing получает в `socks.NewClient`; ни форка sing, ни копии его кода.

Build-tag: нет (базовый socks outbound). Scope: **client + core**, все
платформы. Затронуты все выпуски форка: в `lx.34` sing v0.9.0, в `lx.38`
v0.9.3 — код клиента идентичен.

---

## 1. Проблема

### 1.1 Симптом

LxBox 2.23.1 на ядре `1.14.0-lx.34`, узлы `socks` (version 5, без
`udp_over_tcp`; конфиг сверен LxBox-сессией по дампу): TCP-соединения ходят,
UDP — нет, ошибок в логе нет. Те же прокси в v2rayNG по UDP работают. С
`detour` на MASQUE и без него поведение одинаковое.

### 1.2 Корень: релей диалится по BND.ADDR как есть

`github.com/sagernet/sing` v0.9.3, `protocol/socks/client.go:151-167`
(Version5-ветка `Client.DialContext`), тот же код в v0.9.0 (ядро `lx.34`) и в
апстримной ветке `dev` (последний коммит по файлу 2026-08-21, «Fix socks client
handshake timeout» — нормализации нет):

```go
response, err = ClientHandshake5(tcpConn, command, address, c.username, c.password)
...
udpConn, err = c.dialer.DialContext(ctx, N.NetworkUDP, response.Bind)   // ← как есть
return NewAssociatePacketConn(udpConn, address, tcpConn), nil
```

`response.Bind` — BND.ADDR/BND.PORT из ответа сервера. По RFC 1928 это адрес
релея, но многие реализации (gost, самодельные и большинство публичных прокси
за NAT) отдают `0.0.0.0` или `::` в смысле «тот же хост, что и управляющее
соединение». Дальше два факта, которые вместе дают тихую пропажу:

- **Go документированно шлёт такое на локальную систему.** `net.Dial`: «if the
  host is empty or a literal unspecified IP address, as in ":80", "0.0.0.0:80"
  or "[::]:80" for TCP and UDP … the local system is assumed». Проверено на
  Mac (Darwin 25.3, go1.25.12): `net.Dial("udp", "0.0.0.0:9")` даёт
  `remote=127.0.0.1:9`, `"[::]:9"` — `remote=[::1]:9`. Дайлер ядра
  (`common/dialer/default.go:276-281`) для UDP по IP вызывает обычный
  `net.Dialer.DialContext(ctx, "udp", address.String())` — то же самое; под
  `detour` адрес `0.0.0.0:port` уезжает в detour-плечо и пропадает там.
- **Ошибки нет.** Хендшейк успешен, UDP-сокет «подключён», `WriteTo` возвращает
  успех, ответ не приходит никогда. Наружу это выглядит как мёртвый UDP при
  живом TCP — ровно симптом отчёта.

Xray (`proxy/socks/client.go`, `main` на 2026-09-14, строки 97-100 и 117-119)
делает подстановку явно и поэтому с теми же прокси работает:

```go
if udpRequest.Address == net.AnyIP || udpRequest.Address == net.AnyIPv6 {
	udpRequest.Address = dest.Address
}
udpConn, err := dialer.Dial(ctx, udpRequest.Destination())
```

Проверки порта 0 у Xray нет.

### 1.3 Что подтверждено, что нет

Подтверждён **механизм**: код sing, семантика Go и поведение Xray — всё
проверено по первоисточникам и экспериментом, дефект детерминированный и от
версии ядра не зависит. **Не подтверждено**, что у репортёра именно он: диагноз
поставлен по коду, без дампа UDP-пути и без лога ядра. Картину «TCP ходит, UDP
нет, в Xray ходит» дали бы ещё два сценария, и оба отличимы:

- сервер отвечает реальным IP, а UDP режется дальше (релей отвечает с другого
  порта, NAT продавца) — тогда и Xray бы молчал;
- сервер отвергает ATYP IPv6 в запросе (sing шлёт DST.ADDR `[::]:0` для
  доменных целей, Xray — адрес цели) — тогда в логе ядра был бы
  `socks5: request rejected, code=…`, а не тишина.

Дешёвая проверка до релиза: снять с одного из прокси репортёра ответ на UDP
ASSOCIATE обычным хендшейком — `0.0.0.0` в BND.ADDR закрывает вопрос.

## 2. Решение

Правило Xray в нашем слое (`protocol/socks/udp_associate_lx.go`, новый файл),
**без копии кода sing**: sing сам делает хендшейк и сам диалит релей через
dialer, переданный в `socks.NewClient` — `c.dialer.DialContext(ctx, "udp",
response.Bind)` (`client.go:162`). Единственный UDP-dial через этот dialer —
релей ассоциации, поэтому достаточно обёртки над dialer'ом:

- `relayDialer` (`N.Dialer`): UDP-dial проходит через `relayAddress`, всё
  остальное (TCP к серверу для CONNECT/BIND/UDP ASSOCIATE, `ListenPacket`,
  `Upstream()`) передаётся нижележащему dialer'у как есть. Хендшейк, отмена по
  `ctx`, таймауты остаются в sing и меняются вместе с ним.
- `relayAddress(bind, serverAddr)`:
  - конкретный IP или домен в BND.ADDR — как есть;
  - неопределённый (`0.0.0.0`, `::`) или пустой — адрес сервера из конфига
    (IP или домен) с портом из ответа;
  - порт 0 — ошибка `socks5: udp associate: server replied with relay port 0`;
    диалить порт 0 бессмысленно, а ошибка хендшейка видна в логе.
- Адрес берётся **из конфига, а не из `RemoteAddr()` управляющего соединения**:
  под `detour` удалённый адрес — это пир detour-плеча (сервер
  vless/socks/http), а не SOCKS-сервер. Домен из конфига резолвит тот же
  dialer, что поднял управляющее соединение (`dialer.New(ctx, …,
  ServerIsDomain())` оборачивает его в resolve-dialer), а detour-плечо получает
  обычный UDP-dial по домену. Xray делает так же (`dest.Address`).

Шов в апстримном `protocol/socks/outbound.go` — один блок
`// lx:begin socks-udp-bind … // lx:end socks-udp-bind` в `NewOutbound`, +5
строк: для `Version5` `outboundDialer` заменяется на `newRelayDialer(…)` до
создания `socks.NewClient`; ни одна апстримная строка не меняется. Для
`Version4`/`4a` обёртка не ставится — UDP там отвергает сам sing. `udp_over_tcp`
ходит по TCP через тот же клиент и обёртку не замечает.

Форк sing не заводится: ради пяти строк он стоил бы синка на каждом бампе
базы всего ядра. Копия ветки клиента отвергнута по той же причине — апстрим
правит хендшейк (2026-08-21: отмена по `ctx`), и копия разошлась бы молча.

## 3. Верификация

**`protocol/socks/udp_associate_lx_test.go`** (пакет `socks` ядра):

- `TestLxRelayAddress` — таблица правил §2 (v4/v6 unspecified → IP-сервер и
  доменный сервер, пустой bind, конкретный IP и домен без изменений, порт 0 —
  ошибка).
- `TestLxUpstreamSocksClientDialsUnspecifiedBind` — **страж условия снятия**:
  на записывающем dialer апстримный `socks.Client` диалит `0.0.0.0:2000` как
  есть через переданный dialer. Падает, когда sing начнёт нормализовать
  BND.ADDR сам (§5).
- `TestLxRelayDialerNormalisesBind` — **контракт с sing**: `socks.Client`
  поверх `relayDialer` диалит релей по адресу сервера с портом из ответа
  (IP-сервер, доменный сервер, v4 и v6 unspecified), конкретный и доменный
  BND.ADDR не трогаются, TCP-dial к серверу не изменён, результат —
  `*socks.AssociatePacketConn`. Падает, если sing начнёт диалить релей мимо
  переданного dialer'а — тогда фикс перестал действовать.
- `TestLxRelayDialerRejectsRelayPortZero` — порт 0: ошибка, UDP-dial не
  делается.
- `TestLxNewOutboundUDPThroughUnspecifiedBind` — шов на живом проводе:
  настоящий `NewOutbound`, фейковый socks5-сервер на `127.0.0.1` отвечает
  `[::]:port`, `0.0.0.0:port` и своим настоящим адресом; датаграмма доходит до
  UDP-релея и ответ возвращается с адресом цели; для `version: 4` обёртка не
  ставится — прежняя ошибка sing `udp unsupported`.

Red/green-дискриминатор — записывающий dialer, а не живой стенд: на macOS и
Linux dial на `0.0.0.0:port` уходит на loopback, и релей на `127.0.0.1`
получил бы датаграмму даже на старом коде. Вариант с `[::]:port` при сервере на
`127.0.0.1` красный и вживую (старый код диалит `[::1]:port`).

`go test -race -count=1 ./protocol/socks/` — зелёный; `go vet` и `go build`
пакета под полным `LX_TAGS`, `gofmt -l` по lx-файлам — чисто; сборка без тегов
(все пакеты, кроме игнорируемого gomobile-каталога `build/`) и
`make -f Makefile.lx lx-build` со всем `LX_TAGS` — чистые.

### 3.1 Полевая проверка — открыта

Сборка с фиксом на устройстве репортёра: socks5-узел, UDP-трафик (DNS по UDP,
QUIC) ходит; в логе ядра для UDP-соединений нет `relay port 0`. До этого — снять
BND.ADDR с одного из его прокси (§1.3): `0.0.0.0`/`::` подтверждает диагноз,
конкретный IP отправляет искать дальше (тогда фикс остаётся как паритет с Xray,
но жалобу не закрывает).

### 3.2 Открыто

- Расхождение в **запросе** UDP ASSOCIATE: sing шлёт DST.ADDR `0.0.0.0:0` для
  IPv4-целей и `[::]:0` для доменных и прочих, Xray — адрес цели. Серверы, не
  принимающие ATYP IPv6, отвергнут хендшейк с кодом ошибки; это видно в логе и
  сюда не входит — отдельная тема, если проявится.
- Клиентам (LxBox, лаунчер) делать ничего не нужно: ядровой фикс, конфиг не
  меняется.

## 4. Границы

- Только клиент и только `version: 5`. Серверная сторона (inbound) отвечает
  адресом своего UDP-листенера и не трогается.
- `udp_over_tcp` не затронут: UoT ходит по TCP через прежний `socks.Client`.
- Loopback и приватные адреса в BND.ADDR (`127.0.0.1`, `10.x`) не
  переписываются — как у Xray; сервер, отвечающий своим LAN-адресом из-за NAT,
  остаётся сломанным by design.
- Доменный сервер с несколькими A-записями: TCP и UDP резолвятся одним и тем
  же dialer'ом и в одном порядке, но гарантии «тот же IP» нет — как у Xray.
- Форк sing не заводится; `common/dialer` не трогается.

## 5. Условие снятия

Фикс живёт в новом файле и в одном шве `socks-udp-bind` апстримного
`protocol/socks/outbound.go` (файл апстрим трогает редко). Снимается, когда
sing начнёт нормализовать `response.Bind` в `Client.DialContext` сам — тогда
на бампе sing падает страж `TestLxUpstreamSocksClientDialsUnspecifiedBind`;
после этого снять шов, `udp_associate_lx.go` и его тесты целиком.

**За чем следить на каждом бампе sing** — за контрактом, на котором держится
обёртка: релей диалится через dialer, переданный в `socks.NewClient`, с сырым
BND.ADDR (`protocol/socks/client.go:162`). Если sing начнёт диалить релей
иначе (свой `net.Dial`, `ListenPacket` вместо `DialContext`), обёртка перестанет
видеть этот dial и фикс отвалится молча — это ловит
`TestLxRelayDialerNormalisesBind` («ровно один UDP-dial через dialer, по адресу
сервера»). На мерже `outbound.go` проверять, что шов на месте: встречная правка
апстрима в `NewOutbound` вернёт голый dialer молча, а
`TestLxNewOutboundUDPThroughUnspecifiedBind` (вариант `[::]:port`) это поймает.
