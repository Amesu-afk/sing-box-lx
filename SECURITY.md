# Security Policy / Политика безопасности

## Reporting a vulnerability / Как сообщить

Do not open a public issue. Report it privately through GitHub Security Advisories:

https://github.com/Amesu-afk/sing-box-lx/security/advisories/new

This repository is a downstream copy of sing-box used to build TarnVPN's `libbox`.
Include what you can:

- affected version (`sing-box version`, e.g. `v1.14.2-lx.3`) and platform;
- the feature involved (XHTTP, AWG, MASQUE, VLESS encryption, `lxd`, DNS group, chain, ...);
- steps to reproduce, a minimal config, or a proof of concept;
- impact as you understand it.

For upstream `lx` additions the primary channel is Leadaxe's advisory:

https://github.com/Leadaxe/sing-box-lx/security/advisories/new

For upstream sing-box code:

https://github.com/SagerNet/sing-box/security/policy

### По-русски

Не открывайте публичный issue. Сообщите приватно по ссылке выше и укажите версию,
платформу, затронутую фичу, шаги воспроизведения и влияние. Для upstream-фич `lx`
используйте advisory репозитория Leadaxe; для upstream sing-box — политику SagerNet.

## Scope / Supported versions

This repository carries TarnVPN's downstream packaging and patches. Only the latest
TarnVPN core tag is patched here; upstream fixes come from Leadaxe/sing-box-lx and
SagerNet/sing-box.
