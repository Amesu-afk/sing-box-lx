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
