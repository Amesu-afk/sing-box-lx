# TASKS: 062 — ANDROID_NETWORK_RECOVERY_HANDOVER

- [x] Проследить handover, wake и native reset.
- [x] Добавить регрессионные тесты.
- [x] Подтвердить падение новых тестов на старом scheduler: 4 из 16 scheduler/policy-тестов.
- [x] Исправить очередь recovery; сохранить monitor retries без подтверждения успеха от native callback.
- [x] Проверить обе Android unit-test-варианты: 134 main + 121 legacy, без ошибок; scoped Spotless прошёл.
- [x] Собрать и проверить оба подписанных универсальных APK alpha.66 / 718: package, minSdk 23/21, подписи v1/v2/v3, прежний сертификат и восемь native payloads.
- [x] Зафиксировать результаты и ограничения device-проверки; `adb devices -l` не показывает устройств.
