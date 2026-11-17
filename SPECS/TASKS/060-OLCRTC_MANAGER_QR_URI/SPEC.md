# 060 - OLCRTC_MANAGER_QR_URI

**Фича:** [015-OLCRTC](../../FEATURES/015-OLCRTC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B |
| Статус | C (complete) — parser/runtime, 107 unit-тестов и release APK проверены; живое соединение требует телефона |

## Проблема

QR, создаваемый актуальным Android-клиентом/manager olcRTC, использует форму
`olcrtc://provider@room:1/<room>?client_id=...&dns=...&keepalive=...&key=...`.
TarnVPN разбирает только URI v1 с `?transport@room#key$name`, поэтому ошибочно
считает всё после `olcrtc://` именем provider и отклоняет корректный QR.

## Требования

1. Принимать manager-форму URI и её короткие алиасы параметров.
2. Для Jitsi без `transport` выбирать `datachannel`.
3. Передавать `client_id` как peer-routing channel, `dns` как DNS runtime и
   `keepalive` как интервал liveness.
4. Сохранять исходный URI в sidecar без потери manager-параметров.
5. Не ослаблять проверку provider, transport, 64-символьного hex-ключа,
   обязательного `client_id`, неизвестных и повторяющихся параметров.
6. Сохранить поддержку существующего URI v1.

## Критерии приёмки

- URI со скриншота пользователя разбирается в Jitsi/datachannel профиль;
- room URL сохраняет регистр и полный `https://` путь;
- channel, DNS и keepalive доступны runtime controller;
- unit-тесты, Spotless и release APK проходят;
- фактическое соединение отмечается отдельно, если телефона нет в ADB.
