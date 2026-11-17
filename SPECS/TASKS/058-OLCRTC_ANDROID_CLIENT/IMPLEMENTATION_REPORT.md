# IMPLEMENTATION REPORT - 058

## Реализовано

- добавлен строгий parser URI v1 для `jitsi`, `telemost`, `wbstream` и четырёх
  transport-вариантов с их параметрами;
- одиночные URI и строки из `sub.md` превращаются в обычные профили TarnVPN с
  локальным SOCKS5 outbound;
- `mobile` package olcRTC основан на зафиксированном коммите
  `92b2332769c3dd5000584366201572efc448065f` с локальным Android API и экспортируется одним gomobile bind
  вместе с libbox;
- Android controller запускает olcRTC, ждёт готовности, защищает сокеты через
  `VpnService.protect()`, сохраняет при reload того же URI и закрывает runtime
  при смене профиля/stop/error/destroy;
- список серверов показывает протокол `olcrtc` и RTT активного контрольного
  канала; localhost SOCKS и TCP до WebRTC-провайдера не выдаются за его пинг;
- сохранены существующие ручное обновление и auto-update HTTPS-подписок.

## Проверено 2026-09-16

- `go test ./cmd/internal/build_libbox` — PASS;
- обе AAR (`libbox.aar`, `libbox-legacy.aar`) — PASS, один `go.Seq`/Go runtime;
- `:app:testOtherDebugUnitTest` — PASS, 102 теста;
- `:app:spotlessCheck` — PASS;
- `:app:assembleOtherDebug` — PASS, включая duplicate classes/native packaging;
- arm64 debug APK — 66.24 MiB, universal debug APK — 177.67 MiB.

## Непроверенное поле

Подключённого Android-устройства и живой пользовательской подписки/provider в
момент реализации не было. Поэтому WebRTC negotiation, фактический трафик и
поведение конкретного SFU должны быть подтверждены на телефоне; сборка и юниты
этого не доказывают.

## Дополнение 2026-09-24

- пользователь подтвердил, что Telemost после исправления reload работает;
- нативный RTT-код протестирован: свежий pong, смена сеанса, реконнект,
  устаревший ответ;
- `go test` для `mobile`, `internal/client`, `internal/runtime`, `internal/control`
  прошёл в графе основного модуля;
- обе AAR собраны; Android `testOtherDebugUnitTest` и
  `testOtherLegacyDebugUnitTest` прошли (215 тестов, 0 ошибок суммарно);
- обе release APK-сборки прошли; arm64 APK версии alpha.62 подписан прежним
  сертификатом. Отображение конкретного RTT на телефоне ещё не проверено.
