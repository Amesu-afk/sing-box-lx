# TASKS — 002-XHTTP_CLIENT_TRANSPORT

## v2 — полная клиентская поддержка (ветка `lx-1.14-xhttp-full`)

### Аудит (основание спеки) — ✅ сделано
- [x] Аудит Xray-core `transport/internet/splithttp` + sing-box-extended `transport/v2rayxhttp`/`option`
- [x] Adversarial-верификация каждого из 16 параметров против исходника
- [x] Карта параметров → [PARAM_MAP.md](PARAM_MAP.md) (клиент/сервер, дефолты, on-wire, impl-заметки)
- [x] Классификация: 12 клиентских + 2 tuning-бонуса реализуем; 4 server-only — accept-but-ignore

### Спека (ветка `lx-1.14`) — ✅ сделано
- [x] `SPEC.md` (минимальная) → `SPEC_v1.md` (история сохранена)
- [x] Новая полная `SPEC.md` + `PARAM_MAP.md` — коммит `cafbe546`

### Опции — ✅ сделано
- [x] `option/v2ray_xhttp.go`: +20 полей в `V2RayXHTTPOptions` (placement/key/obfs/tuning + server-only)
- [x] Range-поля строкой `"min-max"` (нет `badoption.Range` в `sing`; решение в SPEC §4.1)
- [x] `option/v2ray_transport.go` уже прокидывает весь `XHTTPOptions` (без новых правок)

### Транспорт — ✅ сделано
- [x] `meta.go`: placement-движок `applyMeta`, `normalizeMeta` (mode-gate'ы, дефолты), uplink-data сборка
- [x] `xpadding.go`: obfs-движок `applyXPadding`, `repeat-x` + `tokenish` (HPACK-Huffman-тюнинг)
- [x] `client.go`: `meta`/`paddingRange`, `NewClient`→`normalizeMeta`, `baseURL`+`newRequest(...)`
- [x] `conn.go`: dial-функции под новую сигнатуру; `packetConn.Write` (разбиение + троттлинг + placement)

### Проверки — ✅ сделано
- [x] `xhttp_test.go` + `url_test.go`: 16 тест-функций (placement/uplink/obfs/tokenish/валидация/регрессия)
- [x] `go test -tags with_xhttp ./transport/v2rayxhttp/` → 16/16 PASS
- [x] `lx-test/config/xhttp_obfs_full.json` + `./sing-box check` → PASS (все 3 xhttp-конфига)
- [x] `make -f Makefile.lx lx-build` → ок; `go vet`/`gofmt` → чисто; tagged/untagged build → ок
- [x] Негатив: без `with_xhttp` → `unknown transport type: xhttp`

### Лайв-верификация
- [x] **Дефолтный путь лайв-подтверждён** на реальных нодах (igareck/vpn-configs): 4 живых XHTTP-сервера,
  packet-up + stream-one(reality), скачивание 1 МБ через IP сервера — закрывает stream-one-TODO из §011
- [ ] **Лайв obfs/placement** против сервера, *настроенного* на obfs (публичные ноды на дефолте — не закрывают)

### Закрытие
- [x] IMPLEMENTATION_REPORT.md (секция v2)
- [ ] Merge ветки `lx-1.14-xhttp-full` → `lx-1.14` (по решению пользователя)

---

## v1 (история) — Complete

### Registry-рефактор (касание upstream) — ✅
- [x] `transport/v2ray/registry.go`: реестр + `RegisterClient` — коммит `e111f800`
- [x] `// lx:` правка `NewClientTransport` — lookup вместо `switch` — коммит `2d97ff56`

### Опции / константа / клиент — ✅
- [x] `V2RayTransportTypeXHTTP="xhttp"` — `2d97ff56`
- [x] lean-native клиент (`client.go`/`conn.go`/`register.go`) — `d1b434fc`
- [x] padding в Referer, sessionId path-layout, stream-one bare-path fix (задача 011) — `5a398a5e`
- [x] Лайв packet-up/auto против Xray/3x-ui (см. IMPLEMENTATION_REPORT v1)

## Восстановление после сна — 2026-10-01

- [x] Реестр физических сокетов переживает замену участников пула.
- [x] Silent retirement очищает заменённых соседей и сохраняет принимающие трафик соединения.
- [x] Полный reset закрывает активные/старые сокеты; завершившийся поздно dial старого поколения отвергается.
- [x] HTTP/2 получает настоящий Close тела upload pipe и освобождает зависшее чтение ответа.
- [x] Общий Android-планировщик учитывает screen/deep/light idle, отмену и wake cooldown.
- [x] Обычное восстановление выполняет native network/DNS reset вместо полного reload ядра.
- [x] Безопасный единичный DoH retry при пересечении reset, без продления caller deadline.
- [x] Локальные HTTP/2-регрессии и Android policy/scheduler-тесты.
- [x] Сборка/проверка обоих AAR и обоих вариантов APK на итоговом дереве: четыре ABI, 10 подписанных APK, Android 23/21.
- [ ] Цикл сон → Telegram/YouTube на физическом телефоне, Wi-Fi и handover.

## Кандидат локализации timeout — 2026-10-19

- [x] Регрессии молчащих исправных соседей, конкурирующих ошибок и позднего timeout старой сети: падение на исходном коде.
- [x] Точечная замена участника вместо общего pool refresh; независимое восстановление разных отказавших участников.
- [x] Сохранение socket ownership и полного network reset; новый маркер member-refreshes.
- [x] Итоговые Go tests/vet и проверки сборки.
- [x] Пересборка обоих AAR и двух универсальных APK alpha.67, проверка native payload/подписи/версии.
- [ ] Сравнение длинных Shorts на физическом телефоне, TestMiami-Test, та же Wi-Fi сеть.

## Общая надёжность открытия и ожиданий — 2026-10-21

- [x] Воспроизведены блокировки HTTP/2 preface после timeout/cancel и поздний ответ; synchronous RoundTrip не гарантировал возврат по таймеру.
- [x] Ограничен сам возврат вызывающему, поздние/отклонённые ответы очищаются без удержания ошибки открытия.
- [x] Исправлен warm-state после закрытия последнего physical socket; сохранён warm при наличии другого сокета.
- [x] Успешный packet-up ack не ждёт произвольный EOF тела ответа.
- [x] Slow/timeout WARN содержит request ID и httptrace-этапы, stable member/generation, inflight/socket count и last-rx.
- [x] Итоговые 70 Go-тестов, включая здоровый принимающий поток на том же сокете при чужом timeout.
- [x] Итоговые CLI/untagged gates, оба AAR и APK alpha.68, native/подпись/metadata.
- [ ] Проверка вкладок браузера и длинных Shorts на установленной alpha.68.

Race-прогон этой добавки не выполнен: Windows Go не нашёл gcc (go-race.log). Это ограничение инструментов, не успешная race-проверка.
