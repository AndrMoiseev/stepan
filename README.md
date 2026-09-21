# stepan
Vendor agnostic AI SDLC Orchestrator

Текущий пользовательский сценарий подготовки спецификации описан в
[протоколе flow `/feature`](docs/feature-flow.md).

## Автономная реализация OpenSpec change

Для подготовленного OpenSpec change предусмотрен отдельный последовательный
implementation loop. Его настройка, правила подготовки ветки, контекстные
команды, восстановление и ограничения платформ описаны в
[руководстве автономной реализации](docs/implementation-loop.md).

Быстрый старт настройки выполняется из корня Git-репозитория:

```text
stepan bootstrap
```

`bootstrap` предлагает изменения `agentruntime.profiles` и `flows.impl_loop` в
пользовательском и проектном JSON-файлах, показывает безопасный diff и сохраняет
его лишь после явного подтверждения. Он не запускает реализацию автоматически.
Режимы `bootstrap` и `/feature` различны: обычный запуск без сохранённого
implementation run пока открывает существующий planning flow. Создание нового
run через `/implement <change>` будет подключено отдельным последующим
изменением; продолжение уже сохранённого run доступно в implementation UI.

## Agent CLI

По умолчанию Stepan запускает `codex` из `PATH`. Другой provider выбирается
явно; без настройки executable используются официальные имена `claude` и
`nessy` из `PATH`:

```text
stepan --agent claude
stepan --agent nessy
```

Для Codex и Claude совместимый корпоративный fork выбирается необязательным
`--agent-cli-name <name>`. Значение должно быть простым именем executable,
доступным в `PATH`; абсолютные пути и directory separators запрещены. Переданное
имя авторитетно и не обязано совпадать с официальным именем provider:

```text
stepan --agent claude --agent-cli-name corporate-claude
```

Совместимость конкретного build пока требует ручной приёмки по
[плану Claude CLI](docs/changes/features/claude-cli-support/manual-test-plan.md); на
текущей машине она имеет статус `BLOCKED`.

### Nessy

Nessy запускается только командой `nessy` из `PATH`. Параметр
`--agent-cli-name` для Nessy запрещён; старый `--agent qwen` удалён.
Перед запуском вручную создайте `~/.stepan/settings.json` (на Windows —
`%USERPROFILE%\.stepan\settings.json`):

```json
{
  "agentruntime": {
    "nessyapp": {
      "auth_token": "replace-with-your-token"
    }
  }
}
```

Stepan читает готовый токен только из этого файла и передаёт каждому процессу
Nessy через `NESSY_CLI_DP_AUTH_TOKEN`, заменяя унаследованное значение.
Переменная окружения и проектные настройки не заменяют домашний файл.
Отсутствующий или некорректный токен останавливает запуск до создания агента;
на запуск Codex и Claude настройки Nessy не влияют.

Получение и замена токена выполняются пользователем. После замены перезапустите
Stepan. Предварительный интерактивный вход, обмен токенов и автоматическое
обновление не выполняются.

Проверка установленного Nessy описана в
[плане приёмки](docs/changes/features/nessy-adapter-auth/manual-test-plan.md).

## Локальные проверки

Для проверки workflow установите закреплённую версию actionlint:

```text
go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

Убедитесь, что каталог `go env GOPATH`/`bin` находится в `PATH`. После этого из
корня репозитория с Go 1.26.5 и GNU Make доступны команды:

| Команда | Проверка |
| --- | --- |
| `make test` | Быстрые изолированные тесты |
| `make test-git` | Тесты с настоящим Git |
| `make test-process` | Тесты управления процессами и fake CLI |
| `make test-all` | Оба интеграционных набора вместе с обычными тестами |
| `make lint-go` | `go vet ./...` |
| `make lint-actions` | Закреплённый `actionlint` для workflow |
| `make lint` | Обе проверки линтерами |

`make` без аргументов показывает доступные цели. Исходные команды и правила
запуска тестов описаны в [AGENTS.md](AGENTS.md).

## CI artifacts

CI всегда запускает быстрые изолированные тесты. Git-интеграции и тесты
управления процессами запускаются отдельными jobs только для затрагивающих их
изменений; при push в `main`, ручном и ежедневном запуске выполняются оба
набора. Локальные команды перечислены в `AGENTS.md`.

GitHub Actions запускает тесты и нативно собирает обе поддерживаемые платформы:

- `stepan-windows-amd64` — ZIP с `stepan.exe`;
- `stepan-darwin-arm64` — `tar.gz` с executable для Apple Silicon Mac.

Артефакты доступны 30 дней на странице нужного запуска в **Actions → CI →
Artifacts**. Каждый архив также содержит `SHA256SUMS` и `BUILD-INFO.txt` с
commit, версией Go и целевой платформой.

Отдельный workflow **Actionlint** проверяет конфигурацию GitHub Actions при
каждом push и pull request.
