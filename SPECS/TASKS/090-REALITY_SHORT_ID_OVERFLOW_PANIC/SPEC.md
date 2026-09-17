# SPEC: 090 — REALITY_SHORT_ID_OVERFLOW_PANIC

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — дефект апстрима, ровесник REALITY (2023), жив в upstream `stable` на момент фикса |
| Статус | I (implemented) — воспроизведён голым `hex.Decode`, закрыт red/green юнитами; отгружается в `v1.14.1-lx.5` |

REALITY `short_id` длиннее 16 hex-символов роняет **весь процесс** паникой
`index out of range [8] with length 8` вместо ошибки конфигурации. Одна и та же
строка в клиентском и серверном конструкторе. Фикс — проверять длину строки **до**
декодирования; всё остальное без изменений.

Build-tag: нет (оба файла и так под `with_utls`). Scope: клиент **и** сервер
(inbound REALITY — второй приоритет форка по конституции, правка та же строка).

---

## 1. Проблема

Найдено DRIFT-инвентарём контракта LxBox/лаунчер на пине `v1.14.1-lx.4`
(TASKS_LXBOX §24.4 г), передано сессией LxBox 2026-09-18.

`common/tls/reality_client.go`, `NewRealityClient`:

```go
var shortID [8]byte
decodedLen, err := hex.Decode(shortID[:], []byte(options.Reality.ShortID))
if err != nil { return nil, E.Cause(err, "decode short_id") }
if decodedLen > 8 { return nil, E.New("invalid short_id") }
```

`encoding/hex.Decode` пишет `len(src)/2` байт в `dst`, не сверяясь с его ёмкостью.
При `len(src) > 16` запись выходит за границы `[8]byte` — паника индекса **внутри**
`hex.Decode`, до того как управление вернётся к проверке `decodedLen > 8`. Проверка
мёртвая: `decodedLen` не может быть больше 8, потому что при 9-м байте процесс уже
упал.

Тот же паттерн в `common/tls/reality_server.go`, `NewRealityServer`, в цикле по
`tls.reality.short_id[]`.

Нечётная длина (`"abc"`) `hex.Decode` ловит сам (`ErrLength`) — ошибкой, не паникой.
Не-hex символы — тоже ошибкой (`InvalidByteError`). Уязвима только длина > 16.

### 1.1 Происхождение

Обе строки — апстрим (blame `70cf681ff2`, `fbc94b9e3e`, 2023-02/03, автор 世界),
наших правок в этом месте нет. В текущем `upstream/stable` код дословно тот же.
Апстрим-контрибуция для форка закрыта — чиним у себя по правилу реестра HOTFIXES.

### 1.2 Зона поражения

| Точка | Путь | Результат до фикса |
|---|---|---|
| `sing-box check -c …` | `NewRealityClient` при сборке outbound'а | паника, exit ≠ 0 без внятной ошибки |
| `sing-box run` / libbox `Start` / `lxd apply` | то же | паника **всего процесса** (VPN падает, а не «узел с ошибкой») |
| inbound `vless` + `tls.reality` на сервере / роутере | `NewRealityServer` | паника при старте |

Клиенты (LxBox §343, лаунчер) отбрасывают такое значение у себя, поэтому
пользователей приложений не бьёт. Бьёт: голое ядро с рукописным конфигом,
подписки с мусорным `sid=` при отключённом клиентском гарде, серверные конфиги.

## 2. Доказательство

- Голый Go (go1.26.8): `hex.Decode` в `[8]byte` от 18 hex-символов →
  `runtime error: index out of range [8] with length 8`.
- Red/green юниты (`common/tls/reality_client_lx_test.go`, `with_utls`): до фикса
  тест на 18-символьный `short_id` роняет процесс тестов паникой, после — получает
  ошибку с текстом `invalid short_id`. Серверная сторона — тем же тестом на
  `NewRealityServer`.

## 3. Требования

1. Клиент: `len(options.Reality.ShortID) > 16` → `E.New("invalid short_id")`
   **до** `hex.Decode`. Проверка `decodedLen > 8` после декодирования снимается как
   мёртвая (либо остаётся — но тест обязан доказывать, что до неё дело не доходит).
2. Сервер: та же проверка на каждый элемент `short_id[]`, с тем же текстом ошибки,
   что рядом (`invalid short_id[<i>]: <value>`).
3. Маркер `// lx: SPEC 090` на обеих правках — файлы апстримные, при мерже
   встречное переписывание блока молча снимет патч.
4. Легальные значения не меняются: пустая строка (= нулевой short_id), 1…16 hex,
   чётная длина. Нечётная и не-hex — ошибка `decode short_id`, как раньше.
5. Текст ошибки не менять: `invalid short_id` — на него могут ориентироваться
   клиентские гарды (LxBox §343).

## 4. Критерии приёмки

- Юниты `with_utls`: 18 hex → ошибка `invalid short_id` (клиент и сервер);
  16 hex → ок; пустая строка → ок; `"abc"` → ошибка decode (не паника);
  `"zz"` → ошибка decode.
- `sing-box check` на конфиге с `short_id: "0123456789abcdef01"` завершается
  ошибкой конфигурации с этим текстом, exit ≠ 0, без стека паники.
- Стенд `lx-test/` не нужен: дефект чисто локальный, без сети.

## 5. Границы

- Провод REALITY не меняется: `short_id` по-прежнему копируется в `SessionId[8:]`.
- Валидатор опций (`option.OutboundRealityOptions`) не трогаем — ошибка остаётся
  на уровне конструктора TLS, как и остальные ошибки REALITY (`invalid public_key`,
  `unknown reality key_share`).
- Сабмодуль `utls` не затрагивается.

## 6. Условие снятия

Апстрим добавит проверку длины `short_id` до `hex.Decode` (или перейдёт на
`hex.DecodeString` + проверку `len`) в `reality_client.go` и `reality_server.go`.
Следить при каждом мерже `upstream/stable`: маркер `// lx: SPEC 090` в обоих файлах,
страж-тесты в `reality_client_lx_test.go`.
