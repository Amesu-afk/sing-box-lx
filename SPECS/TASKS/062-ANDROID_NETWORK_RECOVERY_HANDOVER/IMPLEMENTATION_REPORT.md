# IMPLEMENTATION REPORT: 062 — ANDROID_NETWORK_RECOVERY_HANDOVER

Дата: 14 октября 2026 года.

Устранена подтверждённая локальными регрессиями потеря событий сети в Android recovery. Смена Wi-Fi ↔ мобильная сеть во время выполняющегося reset теперь сохраняется и вызывает один последующий reset после завершения текущего. Быстрые изменения объединяются, интервал ожидания сохраняется, resets выполняются последовательно. Повторный wake во время действующего reset объединяется с ним; после отменённого reset новое событие сети или wake не теряется.

## Реализация

- `clients/android/.../bg/NetworkRecovery.kt`: накопление события отделено от запуска job, добавлен запуск сохранённого handover из `finally`, проверка отмены перед recovery. Wake объединяется только с ещё активной job.
- `clients/android/.../bg/DefaultNetworkMonitor.kt`: `@Volatile` для сетевой ссылки, читаемой другими потоками; прежние повторные уведомления сохранены, поскольку native callback не возвращает подтверждение принятия интерфейса.
- `clients/android/.../bg/NetworkRecoveryTest.kt`: шесть новых регрессий для handover, ошибки reset и отмены.
- `clients/android/version.properties`: локальная сборка `1.14.0-alpha.66`, code `718`, прежняя версия Go.
- `clients/android/releases/v1.14.0-alpha.66.md`: описание подготовленной локальной сборки; alpha.65 сохранена как первоначальный checkpoint.

Пакеты, зависимости, протоколы и build-tag проводка Go не менялись. Все изменения поведения находятся в Android-потребителе. При синхронизации с Android upstream возможная точка ручного переноса — небольшой diff `DefaultNetworkMonitor.kt`; `NetworkRecovery.kt` является собственным recovery-кодом TarnVPN.

## Проверка

1. До исправления четыре новые регрессии падают на старом scheduler: `handoverDuringWakeIsRecoveredAfterWake`, `repeatedHandoversDuringResetShareOneFollowUp`, `failedResetDoesNotDiscardQueuedHandover`, `requestAfterCancelledRunningResetIsNotLost`. Сохранён XML до исправления.
2. После исправления полный Android набор: **134 main + 121 legacy = 255**, failures/errors/skips **0**. В каждой сборке 17 recovery/policy-тестов.
3. Scoped `:app:spotlessKotlinCheck` для трёх изменённых Kotlin-файлов прошёл; `git diff --check` обоих репозиториев прошёл.
4. `go test -tags with_xhttp ./transport/v2rayxhttp -count=1 -timeout 90s` и `go vet -tags with_xhttp ./transport/v2rayxhttp` прошли.
5. Обе release-сборки alpha.66 с lintVital прошли. Полный lint на предыдущем checkpoint alpha.65: **0 errors**, 218 warnings main / 205 legacy. SDK warning в monitor относится к существующей проверке API, нужной legacy-варианту; в alpha.66 снят ранний выход из цикла без изменения API.
6. Оба APK: `app.tarnvpn`, `1.14.0-alpha.66`, code `718`, minSdk **23/21**, подписи **v1/v2/v3** валидны, сертификат совпадает с локальными релизными APK alpha.64.
7. AAR исходник и копии в `app/libs` совпадают по SHA-256. Все восемь `libbox.so` в APK побайтово совпадают с соответствующими native payloads alpha.64; пересборка native ядра для этого Android исправления не требовалась.
8. Проверены SHA-256 двух APK и metadata; source manifest соответствует итоговому дереву.

## Команды

```powershell
$env:JAVA_HOME='C:\Program Files\Android\Android Studio\jbr'
$env:ANDROID_HOME='D:\android-sdk-lx'
$env:GRADLE_USER_HOME='D:\TarnVpn\.gradle-user-home'
$env:PATH=$env:JAVA_HOME+'\bin;'+$env:PATH
.\gradlew.bat :app:spotlessKotlinCheck :app:testOtherDebugUnitTest :app:testOtherLegacyDebugUnitTest --offline --no-daemon --console=plain -I 'D:\TarnVpn\builds\xhttp-idle-audit-20261001\robolectric-offline.gradle' '-PspotlessFiles=**/NetworkRecovery.kt,**/DefaultNetworkMonitor.kt,**/NetworkRecoveryTest.kt'
.\gradlew.bat :app:assembleOtherRelease :app:assembleOtherLegacyRelease :app:lintOtherRelease :app:lintOtherLegacyRelease --offline --no-daemon --console=plain
```

Gradle запускается из `clients/android`. Первоначальный запуск в песочнице не смог подключиться к локальной JVM; проверки выполнены после разрешённого запуска с локальными сокетами. Загрузка зависимостей отключена (`--offline`).

## Артефакты и ограничения

Итоговые артефакты и журналы: `D:\TarnVpn\output\chatgpt-work-vpn-speed-20261014`. Первоначальный checkpoint alpha.65: `D:\TarnVpn\output\chatgpt-work-vpn-fix-20261014`.

- Main universal: SHA-256 `a5dc00e3bb0f6f91a225bbf1cceaefb61af7eac748b69b513b7cc249b0390118`.
- Legacy universal: SHA-256 `09409fbd909e32a103dab8fa542ded080fca9a172bfad705af5b551a684b0801`.
- Подготовлены два APK, `tarn-version-metadata.json` и `SHA256SUMS.txt`.

`adb devices -l` не показывает подключённого телефона. Установка поверх alpha.64, реальный handover, сон/фон, расход батареи и ChatGPT Work на устройстве не проверены. Исправлен дефект очереди recovery; это не доказательство устранения всех возможных зависаний Work. Сетевой reset прерывает текущие прикладные соединения, а восстановление прогресса и остановки зависит также от ChatGPT. Handshake 10/15 секунд и правила XHTTP сохранены.

Сборка локальная: публикация релиза и push/tag в этом запросе не выполнялись.

## Уточнение по скорости и monitor

На alpha.65 пользователь сообщил о FAST 27/36 Мбит/с вместо прежних приблизительно 250/220; переподключение не помогло. Во время проверки выяснилось, что `experimental/libbox/monitor.go` может завершить `UpdateDefaultInterface()` после ошибки поиска интерфейса без исключения и без подтверждения Android-клиенту. Поэтому снят преждевременный ранний выход из Android retry-цикла; устранение потери handover сохранено. Это консервативное исправление контракта callback, а не доказанный фикс пропускной способности.

Пользователь затем сообщил, что сам устранил падение скорости: причина была в конфиге. Клиентская регрессия скорости не установлена; дополнительное сравнение сборок по скорости больше не требуется. Сборка alpha.66 была уже запущена и успешно завершилась, её проверки и файлы сохранены без установки или публикации.
