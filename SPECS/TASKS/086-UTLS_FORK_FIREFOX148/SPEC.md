# SPEC: 086 — UTLS_FORK_FIREFOX148

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — граница фикса [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md): REALITY с `fp=firefox` не проходит на Xray ≥ v26.9.8, потому что в `metacubex/utls` нет пресета Firefox с гибридным key share |
| Статус | N (new) — план утверждён владельцем 2026-09-16: форк `metacubex/utls` сабмодулем + перенос Firefox 148 из `refraction-networking/utls`; конфликты не проверены |
| Ветка | `lx` |
| Связанные | предшествующая [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (§5a — почему без форка не обойтись); issue [#22](https://github.com/Leadaxe/sing-box-lx/issues/22); полевой отчёт [singbox-launcher#124](https://github.com/Leadaxe/singbox-launcher/issues/124); прецедент форк-сабмодуля — [048](../048-GVISOR_HANDSHAKE_NIL_CRASH/SPEC.md) |

**Touches:** `go.mod` (`replace`), `.gitmodules` (`submodules/utls`), CI-чекаут сабмодуля во всех `lx-*.yml`, `common/tls/utls_client.go` (проверка маппинга `"firefox"`), реестр HOTFIXES — строка при реализации.

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
- **R4. CI.** Сабмодуль чекаутится во всех джобах `lx-*.yml` так же, как три существующих; `libbox` и `libbox-legacy` собираются.

## Критерии приёмки

1. Список конфликтов cherry-pick и их разрешение записаны в SPEC.
2. `make -f Makefile.lx lx-build` полным `LX_TAGS`; `go test ./...` с `-ldflags "-checklinkname=0"`; оба AAR — зелёные (linkname в `badtls`/`ktls` резолвятся).
3. ClientHello `fp=firefox`: `X25519MLKEM768` стоит перед `X25519`, классическая часть гибрида и отдельная запись `X25519` — один ключ. Структура приветствия совпадает с тем же пресетом из refraction.
4. REALITY `fp=firefox` против Xray ≥ v26.9.8 → 204; против Xray < v26.9.8 → 204. ⚠️ dest стенда — `swdist.apple.com` или `www.cloudflare.com`, **не** `www.microsoft.com` (серверная ловушка, [комментарий в #22](https://github.com/Leadaxe/sing-box-lx/issues/22#issuecomment-5694711194)).
5. `fp=chrome` без регрессии ([083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md)).

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

## Цена сопровождения

- Четвёртый форк-сабмодуль: дрейф сабмодулей разбирается **до** мержа ядра ([раннбук §1](../../../docs-lx/lx-release-runbook.ru.md)), иначе ядро зелёное, а AAR сломан.
- Бамп `metacubex/utls` в апстриме sing-box → ветка `lx` форка переезжает на новый тег metacubex с сохранением двух коммитов.
- metacubex внешние PR не принимает — синк только своими силами.

## Условие снятия

metacubex выпустит тег с Firefox 148 и reuse, либо апстрим sing-box переедет на библиотеку, где он есть → убрать `replace` и сабмодуль; `"firefox"` уже смотрит в `HelloFirefox_Auto`.
