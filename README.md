# stepan
Vendor agnostic AI SDLC Orchestrator

Текущий пользовательский сценарий подготовки спецификации описан в
[протоколе flow `/feature`](docs/feature-flow.md).

## Agent CLI

По умолчанию Stepan запускает `codex` из `PATH`. Другой provider выбирается
явно; без настройки executable используются официальные имена `claude` и
`qwen` из `PATH`:

```text
stepan --agent claude
stepan --agent qwen
```

Совместимый корпоративный fork выбирается необязательным
`--agent-cli-name <name>`. Значение должно быть простым именем executable,
доступным в `PATH`; абсолютные пути и directory separators запрещены. Переданное
имя авторитетно и не обязано совпадать с официальным именем provider:

```text
stepan --agent claude --agent-cli-name corporate-claude
```

Совместимость конкретного build пока требует ручной приёмки по
[плану Claude CLI](docs/changes/features/claude-cli-support/manual-test-plan.md); на
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
