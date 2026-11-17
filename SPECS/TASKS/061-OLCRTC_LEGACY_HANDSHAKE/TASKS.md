# TASKS - 061

- [x] Подтвердить причину по wire protocol обеих ревизий.
- [x] Закрепить manager-compatible client core и обновить controller.
- [x] Исправить runtime mapping и transport validation.
- [x] Пересобрать и проверить основную Android AAR.
- [x] Поднять версию и собрать подписанный release APK.
- [x] Зафиксировать результаты и ограничения проверки.

## Implementation report

- `olcrtc://` manager profiles use the checked-in client-only v1 handshake
  implementation under `third_party/olcrtc_legacy`.
- The Android AAR exports both `libbox` and `mobile` bindings in one Go
  runtime and was rebuilt for all four Android ABIs.
- `otherRelease` builds as `1.14.0-alpha.55` (`versionCode 707`) and verifies
  with the existing TarnVPN signing certificate.
- Android unit tests are partially verified: 32/42 passed; ten Robolectric
  classes could not initialize because their Android runtime artifact was not
  available in the offline cache. No assertion failure was observed in those
  ten classes.
- Live manager handshake remains device-unverified because no ADB device was
  connected during the build.

## Восстановление после перезапуска сервера

- [x] Воспроизвести таймаут Jitsi и блокировку очереди Telemost на мобильном runtime.
- [x] Исправить ожидание удалённого bridge, начальный EOF и потерю RTP.
- [x] Проверить пакеты Go и собрать обе AAR и подписанные APK alpha.69.
- [x] Проверить HTTPS до/после простоя и повторный вход Jitsi на живом сервере.
- [x] Пользователь подтвердил установку APK alpha.69: оба профиля подключаются, сайты открываются на физическом Android.
