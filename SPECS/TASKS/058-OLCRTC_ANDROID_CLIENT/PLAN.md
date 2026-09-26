# PLAN - 058

1. Добавить изолированный Android-парсер и модель olcRTC URI.
2. Научить subscription parser принимать olcRTC и строить SOCKS5-профиль.
3. Добавить Android runtime-controller вокруг gomobile API.
4. Встроить controller в start/reload/stop lifecycle VPN-сервиса.
5. Связать официальный mobile package с существующей AAR одним bind.
6. Добавить тесты парсера, конфигурации и lifecycle-решений.

## Изменяемые зоны

- новые Android-файлы модели/parser/controller;
- существующие Android importer, repository, service и строки UI;
- `cmd/internal/build_libbox/main.go` - одна tagged/wiring-зона;
- `go.mod`/`go.sum` - точный pin официального olcRTC.

## Ребейз-цена

Core: один аргумент gomobile bind и один build-tag в существующей lx-зоне.
Android downstream: локальные TarnVPN-файлы. Зависимости обновляются только с
парной сборкой обеих AAR.
