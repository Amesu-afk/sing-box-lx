# 061 - OLCRTC_LEGACY_HANDSHAKE

**Фича:** [015-OLCRTC](../../FEATURES/015-OLCRTC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B |
| Статус | V (verified build; live device check pending) |

## Проблема

Manager QR успешно импортируется, но подключение завершается
`read welcome: handshake: read hdr: timeout`. Встроенный официальный core
использует handshake v3 с обязательным challenge, а server/client ветка,
создающая данный QR, закреплена на manager core с handshake v1. Ответ v1 без
challenge отбрасывается v3-клиентом как посторонний до таймаута.

Кроме того, legacy-поле `client_id` является `DeviceID`, а не новым routing
channel.

## Требования

1. Для manager-профилей использовать совместимый manager core и handshake v1.
2. Передавать `client_id` как обязательный DeviceID.
3. Сохранить Jitsi/datachannel, DNS, keepalive, socket protection и lifecycle.
4. Не маскировать несовместимые transport: текущая manager-матрица —
   Jitsi/datachannel и Telemost/vp8channel. WB Stream/vp8channel остаётся
   вне мобильной сборки до включения LiveKit engine.
5. Пересобрать обе AAR и release APK с новым versionCode.

## Критерии приёмки

- AAR экспортирует manager mobile API и содержит один Go runtime;
- Android controller компилируется с singleton API;
- release build и подпись APK проходят; тестовые ограничения явно отмечены;
- live handshake отмечается непроверенным без подключённого телефона.
