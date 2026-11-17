# IMPLEMENTATION REPORT - 060

## Реализовано

- parser различает прежний URI v1 и manager URI с provider в user-info;
- manager query принимает полные имена и короткие алиасы key, transport,
  client ID, DNS, keepalive и VP8-параметров;
- Jitsi без transport получает совместимый default `datachannel`;
- исходный URI сохраняется в sidecar без нормализации и потери query;
- `client_id` передаётся как channel, DNS — через `SetDNS`, keepalive — через
  `SetLivenessOptions` с прежними timeout/failure defaults;
- Jitsi и WB Stream получают 45-секундное окно готовности для многоэтапного
  WebRTC handshake;
- неизвестные/повторные параметры, неверные DNS, keepalive и ключ отвергаются.

## Проверено 2026-09-16

- `:app:spotlessApply :app:testOtherDebugUnitTest` — PASS, 107/107;
- `:app:assembleOtherRelease --rerun-tasks` — PASS, 161 задач выполнена заново;
- APK: package `app.tarnvpn`, version `1.14.0-alpha.54`, code `706`, minSdk 23;
- подписи APK v1/v2/v3 — PASS, сертификат TarnVPN;
- SHA-256 APK:
  `5DF683762D0002D77F768478CB8954EE2D0D5FBA8FC6CE93E4B60145F59BAA67`;
- `git diff --check` — PASS.

## Непроверенное поле

Формат QR закреплён тестом по структуре пользовательского примера, однако ADB-
устройство не подключено. Импорт камерой и WebRTC-соединение с конкретной
комнатой должны быть подтверждены установкой APK на телефон.
