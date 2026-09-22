# SPEC: 092 — INIT_ERROR_NAMES_TAG

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (апстримный файл `box.go`, правка держится по правилу реестра)

| Поле | Значение |
|------|----------|
| Тип | I (improvement) — наблюдаемость ошибок конфигурации для приложений-потребителей (LxBox, лаунчер) |
| Статус | C (complete) — страж в `lx-test/initerr`; выпущено в `v1.14.1-lx.7`, текст сверен на бинарнике из релиза |

Отказ ядра на конфиге называет элемент только индексом: `initialize outbound[0]: invalid short_id`.
LxBox и лаунчер показывают текст ядра как есть, и индекс пользователю ничего не говорит —
у него в подписке сотня узлов с тегами, а не с номерами. Пожелание LxBox от 2026-09-18 после
приёма lx.5. Добавляем к индексу тип и тег в форме имени логгера: `initialize outbound[0]
vless[proxy-de-1]: invalid short_id`.

Build-tag: нет. Scope: `box.New` — все шесть индексированных циклов инициализации.

---

## 1. Проблема

`box.go`, шесть циклов `for i, … := range options.<Kind>` с ошибкой
`E.Cause(err, "initialize <kind>[", i, "]")`:

| Строка | Вид | Логгер, который уже создаётся с типом и тегом |
|---|---|---|
| ~294 | DNS server | `dns/<type>[<tag>]` |
| ~324 | endpoint | `endpoint/<type>[<tag>]` |
| ~343 | inbound | `inbound/<type>[<tag>]` |
| ~361 | service | `service/<type>[<tag>]` |
| ~387 | outbound | `outbound/<type>[<tag>]` |
| ~405 | certificate provider | `certificate-provider/<type>[<tag>]` |

В каждом цикле `tag` уже вычислен (явный тег либо индекс строкой) и `Type` под рукой —
ошибка просто их не использует. Приложения получают текст через `libbox` (`checkConfig`,
`Start`) и через `lxd apply`; оба показывают его пользователю дословно (LxBox §455,
лаунчер).

## 2. Требования

1. В каждом из шести мест: `E.Cause(err, "initialize <kind>[", i, "] ", <Type>, "[", tag, "]")`.
   Префикс `initialize <kind>[<i>]` **сохраняется дословно** — на него могут матчить
   существующие парсеры; новое — только хвост ` <type>[<tag>]` перед двоеточием.
   Пример: `initialize outbound[0] vless[proxy-de-1]: invalid short_id`. Без явного
   тега хвост даёт `vless[0]` — тип всё равно полезен.
2. Один маркер `// lx: SPEC 092` над первым из шести мест и короткий комментарий,
   что формат повторяет имя логгера; на остальных пяти — краткий `// lx: SPEC 092`.
3. Ничего больше в `box.go` не трогать (файл несёт швы других SPEC).
4. Слово «kind» в префиксе не менять (`DNS server`, `certificate provider` — как у
   апстрима, включая регистр).

## 3. Критерии приёмки

- Страж `lx-test/initerr/init_error_names_tag_lx_test.go` (пакет внутри главного
  модуля, как `lx-test/startclose`; контекст через `include.Context`): `box.New` на
  конфиге с outbound'ом `vless`, тег `proxy-de-1`, REALITY `short_id` из 18 hex →
  ошибка содержит `initialize outbound[0] vless[proxy-de-1]: ` и `invalid short_id`;
  второй кейс без тега → `outbound[0] vless[0]`; третий кейс — inbound с заведомо
  битой опцией (например `mixed` с `listen_port` вне диапазона или `tls.enabled` без
  сертификата) → `initialize inbound[0] mixed[<tag>]`. Build-теги теста — те, что
  нужны выбранным протоколам (`with_utls` для REALITY).
- `sing-box check` на таком конфиге печатает новый текст.
- Тесты `common/tls`, `protocol/tuic`, `protocol/masque` не зависят от этого текста
  (они проверяют ошибки конструкторов, а не `box.New`) — прогнать для уверенности.

## 4. Границы

- Формат логгеров, Clash API, gRPC/lxd, libbox-сигнатуры не меняются.
- Ошибки без индекса (`initialize router`, `create log factory`, …) не трогаем.
- Ошибки **внутри** конструкторов (`invalid short_id`, `unknown udp_relay_mode: …`)
  не меняем — тег добавляется обёрткой в `box.New`, а не в каждом протоколе.

## 5. Условие снятия (реестр HOTFIXES)

Апстрим включит тип/тег в текст ошибок инициализации `box.New` (в любой форме). Файл
апстримный, шесть строк рядом с логгерами — при мерже `upstream/stable` проверять маркер
и страж `lx-test/initerr`.
