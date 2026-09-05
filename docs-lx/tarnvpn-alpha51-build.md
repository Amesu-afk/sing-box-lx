# TarnVPN 1.14.0-alpha.51 — сборка 5 сентября 2026

- Android: `4d8c07c940de1b6365b22e8aae64f23be7ee03bd`, versionCode 703.
- Ядро AAR: `189900e6ba28f38cbb379bbab1c9aef1adf3bdb0`, версия `1.14.0-lx.24-189900e6b`.
- Termux: `Amesu-afk/termux-view`, `cfdcae74960517ee0abc0f80f1d15342d0a60410`.
- Go 1.26.5, NDK 28.0.13004108, JDK 17 для gomobile; Android Studio JBR для Gradle 9.3.1 / AGP 9.0.1.
- AAR собраны заново: `go run ./cmd/internal/build_libbox -target android`, затем скопированы в `clients/android/app/libs`.
- APK и проверки: `gradlew :app:testOtherDebugUnitTest :app:testOtherLegacyDebugUnitTest :app:lintOtherRelease :app:lintOtherLegacyRelease :app:assembleOtherRelease :app:assembleOtherLegacyRelease --rerun-tasks --offline --no-daemon --console=plain`.
- Все 328 Gradle-задач выполнены заново. Unit tests: normal 96, legacy 83, без ошибок. Release lint: 0 ошибок, 218/205 предупреждений и по 5 hints. Spotless прошёл отдельно.
- Go tests с `with_xhttp`: transport/v2rayxhttp, experimental/libbox, protocol/group прошли. Сборки `go build ./...`, `go build -tags with_lx_command ./...` и полный набор тегов Makefile.lx прошли.
- Полная сборка приняла xhttp_reality, xhttp_auto_reality, xhttp_obfs_full, awg2_basic, awg2_ranged.
- `go vet` XHTTP/group прошёл. Общий vet libbox сообщает о предсуществующем намеренном unsafe.Pointer в TriggerGoPanic; этот пункт не объявляется зелёным.
- Проверены пакет app.tarnvpn, версия 703, минимальные API 23/21, сертификат обеих APK (совпадает с опубликованной alpha.50). Все четыре libbox.so каждой APK совпадают с независимо обработанными llvm-strip библиотеками из свежей AAR.
- Для релиза подготовлены только universal APK обеих вариаций, metadata, SHA256SUMS и build-info.json с коммитами и хешами.

Полевые проверки на телефоне, обновление поверх установленной версии и реальный XHTTP A/B не выполнены: устройств ADB нет. Долговечный журнал общей транзакции подписки файлов/Room остаётся открытым. Перенос новых upstream-версий отложен: этот выпуск содержит текущие собственные исправления, без изменения базы и форк-сабмодулей ядра.
