# stepan
Vendor agnostic AI SDLC Orchestrator

Текущий пользовательский сценарий подготовки спецификации описан в
[протоколе flow `/idea`](docs/idea-flow.md).

## Agent CLI

По умолчанию Stepan запускает `codex` из `PATH`. Claude Code-совместимый CLI
запускается только с явно заданным абсолютным путём:

```text
stepan --agent claude --agent-cli <absolute-path-to-corporate-cli>
```

Путь может указывать на корпоративный fork и не обязан называться `claude`.
Совместимость конкретного build пока требует ручной приёмки по
[плану Claude CLI](docs/specs/claude-cli-support/manual-test-plan.md); на
текущей машине она имеет статус `BLOCKED`.

## CI artifacts

GitHub Actions запускает тесты и нативно собирает обе поддерживаемые платформы:

- `stepan-windows-amd64` — ZIP с `stepan.exe`;
- `stepan-darwin-arm64` — `tar.gz` с executable для Apple Silicon Mac.

Артефакты доступны 30 дней на странице нужного запуска в **Actions → CI →
Artifacts**. Каждый архив также содержит `SHA256SUMS` и `BUILD-INFO.txt` с
commit, версией Go и целевой платформой.

Отдельный workflow **Actionlint** проверяет конфигурацию GitHub Actions при
каждом push и pull request.
