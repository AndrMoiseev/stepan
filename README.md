# stepan
Vendor agnostic AI SDLC Orchestrator

## CI artifacts

GitHub Actions запускает тесты и нативно собирает обе поддерживаемые платформы:

- `stepan-windows-amd64` — ZIP с `stepan.exe`;
- `stepan-darwin-arm64` — `tar.gz` с executable для Apple Silicon Mac.

Артефакты доступны 30 дней на странице нужного запуска в **Actions → CI →
Artifacts**. Каждый архив также содержит `SHA256SUMS` и `BUILD-INFO.txt` с
commit, версией Go и целевой платформой.

Отдельный workflow **Actionlint** проверяет конфигурацию GitHub Actions при
каждом push и pull request.
