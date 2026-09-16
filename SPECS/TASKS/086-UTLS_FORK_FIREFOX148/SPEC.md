# SPEC: 086 — UTLS_FORK_FIREFOX148

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — граница фикса [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md): REALITY с `fp=firefox` не проходит на Xray ≥ v26.9.8, потому что в `metacubex/utls` нет пресета Firefox с гибридным key share |
| Статус | C (complete) — все пять критериев закрыты 2026-09-16 (dry run релизной матрицы: оба AAR зелёные; полевой прогон на узле репортёра singbox-launcher#124 через лаунчер — `fp=firefox` 204 на Xray ≥ 26.9.8, на v1.14.1-lx.1 тот же узел падал; выпущено в v1.14.1-lx.2). Остаток — AAR на AVD/устройстве (LxBox после бампа пина) и снятие предупреждения §281 для `firefox` версией контракта D-119 синхронно с лаунчером. Реализовано 2026-09-16: форк [Leadaxe/utls-lx](https://github.com/Leadaxe/utls-lx) (`v1.8.7` + `fc716b2` + `ddebe39`, единственный конфликт — блок import), сабмодуль + `replace`, страж-тесты в `common/tls`. Критерии 1, 3, 4, 5 закрыты стендом на Mac (Xray v26.9.9: `fp=firefox` 204, ядро до фикса — `reality verification failed`; v26.7.28/v26.7.11 и `chrome` без регрессии); критерий 2 — локально (сборка полным `LX_TAGS`, `go test ./...`, кросс-сборка android/arm64 с linkname), оба AAR — CI после push суперпроекта. Push и полевой прогон — за владельцем (стоп-точки в «Передаче реализации») |
| Ветка | `lx` |
| Связанные | предшествующая [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (§5a — почему без форка не обойтись); issue [#22](https://github.com/Leadaxe/sing-box-lx/issues/22); полевой отчёт [singbox-launcher#124](https://github.com/Leadaxe/singbox-launcher/issues/124); прецедент форк-сабмодуля — [048](../048-GVISOR_HANDSHAKE_NIL_CRASH/SPEC.md) |

**Touches:** `go.mod` (`replace`, блок `lx:begin utls-firefox148`) + `go.sum` (сняты строки заменённого модуля), `.gitmodules` (`submodules/utls`), `common/tls/utls_client.go` (маппинг `"firefox"` не менялся — проверен тестом), новый `common/tls/utls_firefox148_lx_test.go` (страж), реестр HOTFIXES, `SPECS/README.md`, `README.md`/`README.ru.md` (таблица сабмодулей), `docs-lx/lx-release-runbook.{ru.,}md` §1.1/§3 (четвёртый сабмодуль), `SPECS/CONSTITUTION.md`, `SPECS/IMPLEMENTATION_PROMPT.md`, `docs-lx/lx-changelog.md`.

## Why

[083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) вернула гибридную долю `X25519MLKEM768` в ClientHello, но гибрид есть только в chrome-пресетах `metacubex/utls` v1.8.7. У `HelloFirefox_Auto = HelloFirefox_120` его нет, поэтому узлы с `fp=firefox` в подписке провайдера (пользователь его не меняет) на Xray ≥ v26.9.8 отдают `reality verification failed`. Ждать и просить бесполезно: у metacubex на `master` Firefox 148 нет, issues отключены, из внешних PR не влит ни один — разбор в [083 §5a](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#5a-граница-фикса-не-chrome-отпечатки-исследование-2026-09-16).

## Что переносим — ровно два коммита `refraction-networking/utls`

| Коммит | Что делает |
|---|---|
| `fc716b2` feat: add ff 148 spec and impl keyshare reuse | пресет `HelloFirefox_148` + переиспользование одного классического X25519-ключа в гибридной и классической записях key share |
| `ddebe39` fix: dont add unexported field | переделка той же механики через час: поле `KeyShare.hybridClassicalReuseWith` удалено, признак reuse — байт-маркер в `KeyShare.Data` (`keyShareHybridReuseMarker` / `keyShareClassicalReuseMarker`) |

⚠️ **Только вместе.** Один `fc716b2` — это реализация, от которой апстрим сам отказался. Других коммитов, трогающих Firefox 148 или reuse, в ленте после `fc716b2` нет (`git log -S` по `HelloFirefox_148`, `ReuseMarker`, `ReuseHybridAndClassicalKeyShares`). `6d6e1c6` (ML-KEM в supported groups) меняет только имена в `dicttls/supported_groups.go` — не нужен.

**Почему не в нашем слое.** Reuse — это логика генерации ключей внутри `ApplyPreset` библиотеки. Спека с маркерами, собранная в нашем коде, на metacubex без этой логики ушла бы с однобайтовыми `Data` вместо ключей. Без reuse отпечаток не совпадёт с настоящим Firefox 148, а мимикрия — весь смысл `fp=firefox`.

## Требования

- **R1. Форк сабмодулем.** `metacubex/utls` → `Leadaxe/utls-lx` (по аналогии с `gvisor-lx`, `sing-tun-lx`), ветка `lx` от тега `v1.8.7` (= текущий пин ядра и апстрима), сабмодуль `submodules/utls`, `replace github.com/metacubex/utls => ./submodules/utls`. Путь модуля не меняется — `//go:linkname github.com/metacubex/utls.…` в `common/badtls` и `common/ktls` продолжают резолвиться.
- **R2. Перенос.** Cherry-pick `fc716b2` + `ddebe39` через расхождение веток (с апреля 2025, 34 коммита). Каждый конфликт и его разрешение — в этот SPEC.
- **R3. Маппинг.** На `master` refraction `HelloFirefox_Auto = HelloFirefox_148`; наш `"firefox"` уже смотрит в `HelloFirefox_Auto` (`common/tls/utls_client.go`). После переноса проверить, что алиас переключён, иначе — явно.
- **R4. CI.** Правок workflow нет: все `lx-*.yml` уже чекаутят `submodules: recursive`. Проверить прогоном, что `libbox` и `libbox-legacy` собираются.

## Критерии приёмки

1. Список конфликтов cherry-pick и их разрешение записаны в SPEC. ✅ (Реализация → критерий 1)
2. `make -f Makefile.lx lx-build` полным `LX_TAGS`; `go test ./...` с `-ldflags "-checklinkname=0"`; оба AAR — зелёные (linkname в `badtls`/`ktls` резолвятся). ✅ сборка, тесты и кросс-сборка android/arm64 локально; оба AAR — ✅ dry run `lx-release` [run 35089037951](https://github.com/Leadaxe/sing-box-lx/actions/runs/35089037951) (11 сборочных джоб зелёные, все на go1.26.8)
3. ClientHello `fp=firefox`: `X25519MLKEM768` стоит перед `X25519`, классическая часть гибрида и отдельная запись `X25519` — один ключ. Структура приветствия совпадает с тем же пресетом из refraction. ✅ (Реализация → критерий 3 + страж-тесты)
4. REALITY `fp=firefox` против Xray ≥ v26.9.8 → 204; против Xray < v26.9.8 → 204. ⚠️ dest стенда — `swdist.apple.com` или `www.cloudflare.com`, **не** `www.microsoft.com` (серверная ловушка, [комментарий в #22](https://github.com/Leadaxe/sing-box-lx/issues/22#issuecomment-5694711194)). ✅ стенд 2026-09-16 (Реализация → критерии 4 и 5)
5. `fp=chrome` без регрессии ([083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md)). ✅ (тот же стенд)

## Границы

- Только Firefox 148. Остальные имена (`safari`, `ios`, `android`, `edge`, `360`, `qq`, `random`) — отдельное решение (#22, план п.3).
- В форке — только эти два коммита, собственных lx-правок библиотеки нет.
- Серверная сторона REALITY (`RealityServer` в форке) не трогается; SagerNet/sing-box#4290 не чиним.
- Ядро по-прежнему не подменяет отпечаток ([083 §5](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#5-границы)).

## Передача реализации

**Источники (полные хеши).**
- База форка: `metacubex/utls` тег `v1.8.7` = `f7d52c22f3a8d2f510ad1470f75cb6c3fe26aa37`.
- Переносимые коммиты `refraction-networking/utls`: `fc716b2d1316dbf12aa46f66916061098d6664e4`, затем `ddebe3904b4d7c7f2c6c89181b349f0bb7bd1d00`.

**Ожидаемая зона конфликтов** — генерация key share в `u_parrots.go` (`ApplyPreset`) и `KeyShare`/`KeySharePrivateKeys` в `u_public.go`. Коммит metacubex `800edd4` намеренно генерирует для гибрида **отдельный** ECDHE-ключ (его сообщение само оговаривает: «this will have to change when we support more browsers with different ways of handling this»), а `fc716b2`/`ddebe39` добавляют опциональный reuse ровно там. Поведение существующих пресетов (Chrome — раздельные ключи) не меняется; reuse включается только маркерами Firefox 148.

**Контракт с 083, который нельзя сломать.** `common/tls/reality_client.go` считает `AuthKey` по `KeySharePrivateKeys.Ecdhe`, а при `nil` — по `MlkemEcdhe`. Под reuse оба поля должны остаться заполненными (одним и тем же ключом), иначе `AuthKey` разойдётся с сервером. Нужен тест на Firefox 148.

**CI.** Все `lx-*.yml` чекаутят `submodules: recursive` — новый сабмодуль подхватится без правок workflow; только проверить прогоном.

**Метод для критерия 3.** В тесте форка после `BuildHandshakeState` сравнить: порядок расширений, группы key share (`X25519MLKEM768` перед `X25519`), совпадение X25519-хвоста гибрида с классической записью. Эталон refraction собирать в отдельном scratch-модуле — в `go.mod` ядра `refraction-networking/utls` не добавлять.

**Стенд для критерия 4** — как в [083 §2](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#2-доказательство) (Xray на loopback, v26.9.9 и v26.7.28), dest не `www.microsoft.com`.

**Только с явного «да» владельца.**
- Создание репозитория `Leadaxe/utls-lx` на GitHub и первый push.
- Порядок push: сабмодуль → суперпроект (иначе «not our ref» во всех джобах).
- Тег/релиз не резать; другие отпечатки не трогать; полевой прогон — за владельцем.

## Реализация — 2026-09-16

### Форк `Leadaxe/utls-lx`

- Самостоятельный репозиторий (не GitHub-fork — как `gvisor-lx`/`sing-tun-lx`), ветка `lx`
  (default) от тега `v1.8.7` = `f7d52c22f3a8d2f510ad1470f75cb6c3fe26aa37`; тег `v1.8.7` запушен
  вместе с веткой, чтобы база сверялась с remote (`git describe` → `v1.8.7-2-g6b7f051`). История
  metacubex взята целиком — клон 9.9 МБ, снапшот как у gvisor не нужен.
- Коммиты поверх базы: `9fd088e` = `cherry-pick -x fc716b2`, `6b7f051` = `cherry-pick -x ddebe39`.
  Собственных правок библиотеки нет; апстримный workflow `go.yml` metacubex не трогался.
- `go.mod` форка не менялся: metacubex держит `go 1.20`, refraction — `go 1.24`, но перенесённый код
  ничего новее 1.20 не использует (`crypto/ecdh` есть с 1.20; ML-KEM — через metacubex-овский
  `internal/mlkem`, см. конфликт ниже). Сборка под go1.26.8 идёт с языковой версией модуля 1.20 —
  проверено.
- Проверка форка: `go build ./...`, `go vet .`, `go test ./...` — весь набор metacubex плюс
  перенесённый `u_parrots_test.go` (`TestParrotFingerprintsReuseHybridClassicalKeyShare`,
  `TestHybridClassicalKeySharesAreIndependentByDefault`) — зелёные под go1.26.8.
- Подключение в ядре: сабмодуль `submodules/utls` (`.gitmodules`: `branch = lx`), `replace
  github.com/metacubex/utls => ./submodules/utls` (блок `lx:begin utls-firefox148` в `go.mod`);
  `go list -m github.com/metacubex/utls` → `v1.8.7 => ./submodules/utls`. Из `go.sum` сняты две
  строки `metacubex/utls v1.8.7` (h1 и /go.mod) — ровно то, что предлагает `go mod tidy -diff`;
  для директорийного `replace` хеш не проверяется, у `sing-tun`/`gvisor` таких строк тоже нет.
  (`tidy -diff` попутно показывает давно лишние строки `sagernet/wireguard-go v0.0.6` — чужой
  хвост, здесь не трогался.)

### Критерий 1 — конфликты cherry-pick

| Коммит | Конфликт | Разрешение |
|---|---|---|
| `fc716b2` | один, `u_parrots.go`, блок `import`: refraction добавляет `"crypto/ecdh"` рядом со своим `"crypto/mlkem"`, а у metacubex `crypto/mlkem` нет — ML-KEM живёт в `github.com/metacubex/utls/internal/mlkem` (совместимость с go1.20; `crypto/mlkem` появился в Go 1.24) | оставлен metacubex-овский `internal/mlkem`, добавлен только `"crypto/ecdh"`; API совпадает (`mlkem.SeedSize`, `NewDecapsulationKey768`, `EncapsulationKey().Bytes()`), тело коммита легло без правок |
| `ddebe39` | нет | — |

Ожидавшийся конфликт в зоне генерации key share **не случился**: регион `case *KeyShareExtension`
в `ApplyPreset` у metacubex `v1.8.7` и у refraction на `fc716b2^` текстуально идентичен (сверено
`diff`), структуры `KeyShare` и `KeySharePrivateKeys` — тоже; `800edd4` (раздельный ECDHE-ключ
для гибрида) лежит в обеих ветках после точки расхождения (`9dd2a0b`, 2025-04-20). Дельта форка
= дельта двух коммитов refraction + одна строка импорта.

### Критерий 3 — ClientHello Firefox 148 против refraction

Метод — по разделу «Передача реализации»: scratch-модуль вне ядра (в `go.mod` ядра refraction не
добавлялся) с двумя импортами — `github.com/metacubex/utls` (→ форк через `replace`) и
`github.com/refraction-networking/utls` на пине Xray `v1.8.3-0.20260301010127-aa6edf4b11af`
(потомок `ddebe39`). У обоих `UClient(…, HelloFirefox_148)` + `BuildHandshakeState()` с
детерминированным `Config.Rand`, затем разбор **сырого** ClientHello по байтам, без библиотечных
типов: версия, длины random/session_id, cipher suites, compression, порядок и содержимое
расширений (для key_share — группы и длины записей).

Результат: `HelloFirefox_Auto` = `148` у обоих (R3 закрыт — `"firefox"` в `utls_client.go`
менять не пришлось); 15 расширений одного типа в одном порядке; форма приветствия идентична:

| Поле | Значение (одинаково у форка и refraction) |
|---|---|
| cipher_suites | `1301 1303 1302 c02b c02f cca9 cca8 c02c c030 c00a c009 c013 c014 009c 009d 002f 0035` |
| расширения по порядку | sni, extended_master_secret, renegotiation_info, supported_groups, ec_point_formats, alpn (`h2`, `http/1.1`), status_request, delegated_credentials (`0403 0503 0603 0203`), sct, key_share, supported_versions (`0304 0303`), signature_algorithms (`0403 0503 0603 0804 0805 0806 0401 0501 0601 0203 0201`), record_size_limit (`0x4001`), compress_certificate (zlib, brotli, zstd), ECH GREASE (281 байт, payload 239) |
| supported_groups | `X25519MLKEM768 (11ec), X25519 (001d), P-256, P-384, P-521, ffdhe2048, ffdhe3072` |
| key_share | `X25519MLKEM768: 1216 байт, X25519: 32, P-256: 65` — гибрид **перед** X25519, каждый по разу |
| reuse | последние 32 байта гибридной записи == запись `X25519` (проверено на обеих библиотеках) |

Единственное различие сырых байтов — AEAD в ECH GREASE (`0x0001`/`0x0003`): обе библиотеки
выбирают его монетой из одного набора кандидатов (`AES-128-GCM`, `ChaCha20-Poly1305`; за 40
приветствий — lx 9/31, refraction 17/23). Это случайность внутри одного пресета, не структура.

### Страж в ядре — `common/tls/utls_firefox148_lx_test.go` (`with_utls`)

- `TestLxFirefoxFingerprintIsFirefox148` — `"firefox"` → `HelloFirefox_Auto` == `HelloFirefox_148`
  (R3; на голом metacubex упадёт: там `Auto = 120`).
- `TestLxRealityFingerprintsCarryHybridShareFirst` — для `chrome` и `firefox`: `X25519MLKEM768`
  перед `X25519` и ровно по одному разу в key_share и в supported_groups, длины записей 1216/32.
  Это же автоматизирует проверку из [083 §6](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#6-условие-снятия)
  («`HelloChrome_Auto` несёт гибрид перед X25519») — теперь она падает в CI, а не в поле.
- `TestLxFirefox148ReusesClassicalKeyAcrossShares` — X25519-хвост гибрида == запись `X25519`;
  `KeySharePrivateKeys.Ecdhe` и `.MlkemEcdhe` оба не nil и **один и тот же** объект
  (`require.Same`), публичный ключ `Ecdhe` == записи `X25519` — контракт `AuthKey` из 083
  (`Ecdhe`, при nil — `MlkemEcdhe`) под reuse держится: сервер возьмёт чистый X25519, мы считаем
  по тому же ключу.

Три теста зелёные (`go test -tags with_utls -ldflags "-checklinkname=0" ./common/tls/`).

### Критерий 2 — сборка и тесты ядра

- `make -f Makefile.lx lx-build` полным `LX_TAGS` (go1.26.8, darwin/amd64) — ✅, версия
  `1.14.1-lx.1`.
- `go test ./...` под `LX_TAGS` с `-ldflags "-checklinkname=0"` — ✅ 39 пакетов ok. В первом прогоне три
  пакета упали по таймингам при load average ≈35 (параллельная сборка + сторонняя нагрузка на Mac):
  `common/tls` — пять Apple-платформенных `TestAppleClient*` (`context deadline exceeded`), `dns` —
  `TestDNSEvaluateParallelFallback`/`TestDNSLogicalRace` («436ms is not less than 350ms»),
  `dns/transport/group` — `TestTargetBudgetLeavesRoomForRescue`/`TestTraceElectionFanned`. Повтор
  тех же тестов на разгруженной машине — все три пакета ok; к utls отношения не имеют.
- linkname `badtls`/`ktls` на android — ✅ кросс-сборка `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go
  build ./cmd/sing-box` с тегами AAR (`cmd/internal/build_libbox/main.go`, без `with_naive_outbound`:
  cronet без cgo не собирается, к linkname это не относится) и `-ldflags "-checklinkname=0"` —
  слинковалась (ELF aarch64 для android). Это проверка резолва linkname под android-таргет, не
  замена AAR: сам gomobile-билд — в CI.
- Оба AAR — ✅ dry run `lx-release` (run 35089037951, 2026-09-16): `build android (libbox.aar)` зелёная вместе с остальными десятью сборочными джобами (darwin ×2 cgo naive, windows ×3 incl. Win7/386, linux mips, musl ×4), все на go1.26.8, публикация пропущена.

### Полевой прогон (лаунчер, 2026-09-16)

Агент singbox-launcher собрал ядро из `lx` (`submodules/utls` = `6b7f051`, `LX_VERSION=1.14.1-lx.2-local`, штатный `LX_TAGS`) и прогнал реальный узел репортёра [singbox-launcher#124](https://github.com/Leadaxe/singbox-launcher/issues/124) (`ger10.nekosocks.com:443`, vless+reality+vision, `fp` из подписки): на релизном `1.14.1-lx.1` — `firefox` → `reality verification failed`, `chrome` → 204; на `lx.2-local` — `firefox` 204 ×3 и `api.ipify.org` 200, `chrome` 204 ×3, в логе ни одного `reality verification failed` ([комментарий в #124](https://github.com/Leadaxe/singbox-launcher/issues/124#issuecomment-5696611981)). Мобильная сторона: LxBox бампит пин после тега и проверяет AAR на AVD против стенда; предупреждение §281 для `firefox` снимается не односторонне, а версией контракта D-119 (`contract/README.md`, `warnings.json`, корпус-кейс `reality_fp_firefox_kept`) синхронно с лаунчером, с пометкой «с v1.14.1-lx.2» — на апстримном `metacubex/utls` имя `firefox` гибрида по-прежнему не несёт.

### Критерии 4 и 5 — стенд (Mac, 2026-09-16)

Как в [083 §2](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#2-доказательство): Xray-сервер на loopback
(`127.0.0.1:14443`, VLESS + REALITY, `xtls-rprx-vision`, `show: true`), `dest`/`serverNames` =
`www.apple.com` — тот же стенд, что закрыл 083, чтобы «до/после» сравнивались один в один, — плюс
контрольный прогон с `www.cloudflare.com` (один из двух dest, названных в критерии 4). Ядро «после» =
эта ветка (`sing-box-086`, darwin/amd64, полный `LX_TAGS`); ядро «до» = сборка со стенда 083 (фильтр
083 снят, utls — стоковый metacubex v1.8.7). `mixed`-inbound, `curl -x socks5h://…
http://www.gstatic.com/generate_204` ×3 на отпечаток. Пробник ждёт открытия портов, а не спит
фиксированные паузы: первый прогон под load average ≈35 от параллельного `go test ./...` дал `000`
по всем клеткам, включая базовые из 083, — стенд, не ядро.

| Ядро | Xray | chrome | firefox |
|---|---|---|---|
| **после (086)** | **v26.9.9** | 204 204 204 | **204 204 204** |
| до (083, стоковый utls) | v26.9.9 | 204 204 204 | ✗ ✗ ✗ `reality verification failed` |
| после (086) | v26.7.28 | 204 204 204 | 204 204 204 |
| после (086) | v26.7.11 | 204 204 204 | 204 204 204 |
| до (083) | v26.7.28 | — | 204 204 204 |
| после (086), dest `www.cloudflare.com` | v26.9.9 | 204 204 204 | 204 204 204 |

- Лог Xray v26.9.9 «после», каждое соединение: `hs.c.AuthKey[:16]: […]`, `hs.c.ClientVer: [26 3 27]`,
  `hs.c.conn == conn: true`, затем `accepted tcp:www.gstatic.com:80` — сервер посчитал `AuthKey` по
  тому же ключу, что и мы: reuse не разошёлся с контрактом 083 (критерий 4 ✅).
- «До» с `fp=firefox`: `forwarded SNI: www.apple.com` без `AuthKey`, затем `processed invalid
  connection … authentication failed or validation criteria not met` — та же отсечка, что в 083 §2,
  теперь только у стокового utls.
- Строка `processed invalid connection … failed to read client hello` в начале каждого лога Xray —
  это проверка открытого порта пробником (`nc -z`), не клиент.
- `fp=chrome` — 204 на всех трёх версиях Xray, регрессии 083 нет (критерий 5 ✅).

### CI

Правок workflow нет (R4): все `actions/checkout` в `lx-*.yml` уже с `submodules: recursive`
(сверено по каждому чекауту, включая `lx-rebase.yml` с явным `git submodule update --init
--recursive`). Единственный чекаут без сабмодулей — job публикации релиза в `lx-release.yml`,
который Go не собирает.

## Цена сопровождения

- Четвёртый форк-сабмодуль: дрейф сабмодулей разбирается **до** мержа ядра ([раннбук §1](../../../docs-lx/lx-release-runbook.ru.md)), иначе ядро зелёное, а AAR сломан.
- Бамп `metacubex/utls` в апстриме sing-box → ветка `lx` форка переезжает на новый тег metacubex с сохранением двух коммитов.
- metacubex внешние PR не принимает — синк только своими силами.

## Условие снятия

metacubex выпустит тег с Firefox 148 и reuse, либо апстрим sing-box переедет на библиотеку, где он есть → убрать `replace` и сабмодуль; `"firefox"` уже смотрит в `HelloFirefox_Auto`.
