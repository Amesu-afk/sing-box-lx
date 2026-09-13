# SPEC: 083 — REALITY_MLKEM_KEYSHARE

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный костыль, протухший вместе с зависимостью; жив в upstream (issue [SagerNet/sing-box#4520](https://github.com/SagerNet/sing-box/issues/4520) без реакции) |
| Статус | V (verified) — стенд на Mac 2026-09-13: Xray v26.9.9 (после отсечки), v26.7.28 и v26.7.11 (до); выпущено в v1.14.0-lx.36 |
| Связанные | [053](../053-REALITY_MIN_CLIENT_VER/SPEC.md) — предыдущая тихая отсечка REALITY, закрыта полевым прогоном на этом же стенде |

Xray-сервер начиная с v26.9.8 отвергает ClientHello, в котором нет key_share
`X25519MLKEM768`. Апстримный sing-box сам вырезает этот шар из приветствия перед
рукопожатием и потому **тихо** отбрасывается — подменой на камуфляжный сайт,
на клиенте это `reality verification failed`. Фикс — перестать вырезать шар и
считать `AuthKey` по тому же ключу, что выбирает сервер.

Build-tag: нет (правка внутри `with_utls`). Scope: **client-only**.

---

## 1. Проблема

### 1.1 Что изменилось на стороне Xray

Коммит `XTLS/REALITY@8cdf7bf` (RPRX, 2026-09-08, «Reject outdated/strange Client
Hello that doesn't have X25519MLKEM768 before optional X25519») переписал выбор
пирового ключа в `tls.go` (`Server()`):

```go
var peerPub, peerPub2 []byte
for _, keyShare := range hs.clientHello.keyShares {
    if keyShare.group == X25519MLKEM768 && len(keyShare.data) == mlkem.EncapsulationKeySize768+32 {
        if peerPub2 != nil { peerPub2 = nil; break } // ensure once
        peerPub2 = keyShare.data[mlkem.EncapsulationKeySize768:]
        continue
    }
    if keyShare.group == X25519 && len(keyShare.data) == 32 {
        if peerPub != nil { peerPub2 = nil; break } // ensure once
        peerPub = keyShare.data
        break // ensure order
    }
}
if peerPub2 == nil {
    break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
}
if peerPub == nil {
    peerPub = peerPub2 // secondary choice: X25519 in X25519MLKEM768
}
```

Три следствия:

- гибридный шар **обязателен**, и он должен стоять **раньше** чистого X25519
  (`break // ensure order` после X25519 не даёт увидеть гибрид, идущий следом);
- каждый из двух шаров допустим **не более одного раза**;
- `AuthKey` считается по чистому X25519, если он есть, иначе — по X25519-части
  гибрида (`peerPub2`). До коммита порядок был тот же, но без обязательности.

Xray-core пинит `github.com/xtls/reality v0.0.0-20260908062103-8cdf7bf9c7f0`
в **v26.9.8** и **v26.9.9**; v26.7.28 и раньше — `…20260322125925-9234c772ba8f`
(до отсечки). Обновлённый сервер получает отсечку без настройки и без ручки.

### 1.2 Почему у нас нет гибридного шара

`common/tls/reality_client.go`, `ClientHandshake`: после `BuildHandshakeState`
апстрим проходит по расширениям и **вырезает** `X25519MLKEM768` из
`SupportedCurvesExtension` и `KeyShareExtension`, затем собирает состояние
заново. Это костыль апстримного коммита `dbdcce20a` «Update utls to v1.7.2»:
та utls не различала X25519-ключ гибрида и чистого шара в `KeyShareKeys`, и
`AuthKey`, посчитанный по неверному ключу, ломал верификацию. Проще было
убрать гибрид совсем — REALITY-серверы того времени его не требовали.

Апстрим sing-box сломан ровно так же (issue #4520, 2026-09-11). Именно про этот
фильтр RPRX пишет как про «обрезку» ClientHello у клиентов sing-box.

### 1.3 Почему теперь безопасно снять

Наш пин — `github.com/metacubex/utls v1.8.7`. Там ключи разложены по
раздельным полям (`u_public.go`):

```go
type KeySharePrivateKeys struct {
    CurveID    CurveID
    Ecdhe      *ecdh.PrivateKey        // чистый X25519 (первый не-GREASE, не-гибрид)
    Mlkem      *mlkem.DecapsulationKey768
    MlkemEcdhe *ecdh.PrivateKey        // X25519-часть X25519MLKEM768
}
```

`u_parrots.go` при сборке `KeyShareExtension` кладёт ключ гибрида в
`Mlkem`/`MlkemEcdhe`, а первый обычный шар — в `Ecdhe`; путаницы utls 1.7.2
больше нет. Спека `HelloChrome_133` (= `HelloChrome_Auto`, все `chrome*`
имена ядра) шлёт шары в порядке `GREASE, X25519MLKEM768, X25519` — ровно то,
что требует сервер, каждый по одному разу.

Выбор ключа для `AuthKey` зеркалит серверный порядок: `Ecdhe`, а при `nil` —
`MlkemEcdhe`. Так же считают Xray-клиент и mihomo. Для Chrome в ClientHello
есть чистый X25519 → обе стороны берут его; для отпечатка только с гибридом
обе стороны взяли бы X25519-часть гибрида. Совпадение по построению.

Старые серверы регрессии не получают: клиент шлёт оба шара, REALITY до
`8cdf7bf` берёт первый чистый X25519 — тот же ключ. Подтверждено прогоном (§2).

### 1.4 Симптом

Тот же класс, что в SPEC 053 §1.3: провал условия не рвёт соединение, сервер
прозрачно проксирует его на `dest` с настоящим сертификатом, клиент видит
`uConn.Verified == false` → `reality verification failed`. На стороне Xray
(`show: true`) — только `forwarded SNI` без строк `AuthKey`/`ClientVer`, затем
`processed invalid connection … authentication failed or validation criteria
not met`. Неотличимо от чужого ключа или неверного shortId.

### 1.5 Зона поражения

Все REALITY-клиенты форка (VLESS, trojan, vmess, XHTTP-поверх-REALITY): фильтр
живёт в одном месте. После снятия фильтра судьбу узла решает **отпечаток**:
гибридный шар несут только спеки, где он заложен.

| `utls.fingerprint` ядра | Спека utls v1.8.7 | key_share | Xray ≥ v26.9.8 |
|---|---|---|---|
| `chrome`, `""`, `chrome_psk`, `chrome_psk_shuffle`, `chrome_padding_psk_shuffle`, `chrome_pq`, `chrome_pq_psk` | `HelloChrome_133` | GREASE, **X25519MLKEM768**, X25519 | ходит |
| `firefox` | `HelloFirefox_120` | X25519, P-256 | отвергается |
| `edge` | `HelloEdge_85` | X25519 | отвергается |
| `safari` | `HelloSafari_16_0` | X25519 | отвергается |
| `ios` | `HelloIOS_14` | X25519 | отвергается |
| `android` | `HelloAndroid_11_OkHttp` | X25519 | отвергается |
| `360` | `Hello360_7_5` | X25519 | отвергается |
| `qq` | `HelloQQ_11_1` | X25519 | отвергается |
| `random` | один из Chrome/Firefox/Edge/Safari/iOS на процесс | как у выпавшего | ходит в 1 случае из 5 |
| `randomized` | генератор `HelloRandomized` | гибрид добавляется монетой `KeyShare_Append_RandomGroups` = 0.5 | ~половина процессов |

Гибрид есть только у `HelloChrome_131`/`HelloChrome_133`; `HelloChrome_115_PQ`/
`120_PQ` несут `X25519Kyber768Draft00`, который сервер не считает за гибрид.
Это паритет с самим Xray: там после `8cdf7bf` тоже работают только
Chrome-отпечатки. Ядро отпечаток **не подменяет** (см. Границы); приложение
подменяет на своём уровне — LxBox §281.

## 2. Доказательство

Стенд 2026-09-13, macOS arm64, всё на loopback: Xray-сервер (VLESS+REALITY,
`xtls-rprx-vision`, `dest`/`serverNames` = `www.apple.com`, `show: true`),
ядро `go build -tags "with_utls with_quic with_gvisor" ./cmd/sing-box` до и
после патча, `mixed`-inbound, `curl -x socks5h://… http://www.gstatic.com/generate_204`
×3 на каждый отпечаток.

| Ядро | Xray | chrome | firefox | safari | ios | random |
|---|---|---|---|---|---|---|
| до патча | v26.9.9 | ✗ verification failed | ✗ | ✗ | ✗ | ✗ |
| **после патча** | **v26.9.9** | **204 204 204** | ✗ (нет гибрида) | ✗ | ✗ | ✗ |
| до патча | v26.7.28 | 204 | 204 | 204 | 204 | 204 |
| после патча | v26.7.28 | 204 | 204 | 204 | 204 | 204 |
| до патча | v26.7.11 | 204 | 204 | 204 | 204 | 204 |
| после патча | v26.7.11 | 204 | 204 | 204 | 204 | 204 |

- В логе Xray v26.9.9 до патча: `forwarded SNI: www.apple.com` без `AuthKey`,
  далее `authentication failed or validation criteria not met` — отсечка по
  ключу, не по версии/shortId. После патча для chrome — `hs.c.conn == conn: true`.
- Контроль Xray v26.9.9 «сам с собой» (клиент Xray, `fingerprint: chrome`) — 204.
- **SPEC 053 закрыта тем же стендом**: v26.7.11 — первый релиз с дефолтом
  `minClientVer = 26.3.27`. Наше ядро (`26.3.27`) — 204 на всех отпечатках;
  контрольная сборка с апстримными `1.8.1` — `hs.c.ClientVer: [1 8 1]`,
  `hs.c.conn == conn: false`, `reality verification failed`.
- Ловушка стенда: с `dest = www.microsoft.com:443` серверы REALITY **до**
  `8cdf7bf` (v26.3.27, v26.7.11, v26.7.28) не завершают рукопожатие даже с
  Xray-клиентом той же версии (`handshake did not complete successfully` сразу
  после `Certificate: 8273`). В ленте REALITY тех же суток есть #33 «Increase
  target TLS record buffer to 17 KiB» — вероятная причина; к нашему клиенту
  отношения не имеет, `dest` для стенда брать с коротким сертификатом.

Unit: `go test -tags with_utls ./common/tls/` зелёный; `go vet` чистый.

## 3. Требования

- `ClientHandshake` не трогает `SupportedCurvesExtension`/`KeyShareExtension`:
  отпечаток шлёт то, что заложено в его спеке utls. Второй
  `BuildHandshakeState` не нужен.
- `AuthKey` считается по `keyShareKeys.Ecdhe`, при `nil` — по
  `keyShareKeys.MlkemEcdhe`; `nil` обоих — прежняя ошибка `nil ecdheKey`.
- Совместимость со старыми серверами сохраняется без ручек в конфиге.
- Маркер `// lx: SPEC 083` на обеих правках — файл апстримный и активно
  правится, встречный мерж не должен вернуть фильтр молча.

## 4. Критерии приёмки

- Сборка и тесты `common/tls` под `with_utls` зелёные. ✅
- Против Xray ≥ v26.9.8 с `fingerprint: chrome` — HTTP через туннель. ✅ (§2)
- Против Xray до `8cdf7bf` (v26.7.11, v26.7.28) — все отпечатки без регрессии. ✅ (§2)
- SPEC 053: полевой критерий «Xray ≥ v26.7.11 с незаданным `minClientVer`
  ходит» — ✅ (§2), статус 053 переведён.

## 5. Границы

- Серверная сторона REALITY (`reality_server.go`) вне scope — форк client-focused.
- Ядро **не подменяет** отпечаток при REALITY: `fingerprint` — явный выбор
  пользователя, а Xray сам ведёт себя так же (не-Chrome отпечатки там тоже
  мертвы после `8cdf7bf`). Подмена и предупреждение — уровень приложения:
  LxBox §281 (при REALITY отпечаток не из chrome-семейства → `chrome` +
  предупреждение на ноде). Ручку «принудительно chrome при reality» в ядро
  не вводим — у выбора нет второго осмысленного значения.
- ML-DSA-65 (`mldsa65Seed` на сервере / `mldsa65Verify` у клиента,
  Xray ≥ v25.x) — необязательная постквантовая подпись сертификата поверх
  ed25519-HMAC; без неё верификация идёт как раньше. К этой поломке отношения
  не имеет, в scope не входит.
- `X25519Kyber768Draft00` (спеки `chrome_pq` старых utls) сервером за гибрид не
  считается — ничего не делаем, у нас эти имена и так схлопнуты в `HelloChrome_133`.

## 6. Условие снятия

Апстрим sing-box уберёт фильтр в `common/tls/reality_client.go` и добавит
fallback на `MlkemEcdhe` (issue #4520) — тогда наши два маркера снимаются
в пользу апстримной формы. Пока висит: проверять на каждом мерже, что фильтр
не вернулся (grep `X25519MLKEM768` по файлу должен давать ноль), и что
`HelloChrome_Auto` в новой utls по-прежнему несёт гибрид **перед** X25519 —
смена порядка в спеке utls сломает узлы так же тихо.
