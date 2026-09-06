# ADR 0004: Claude Code-совместимый runtime через Go SDK

Статус: **принято**  
Дата: 2026-08-25

## Контекст

Stepan должен поддерживать второй agent CLI, в том числе корпоративный fork
Claude Code с другим basename и branding. Прикладной `/feature` flow не должен
зависеть от SDK или от имени provider. На машине разработки нет доступа к
корпоративному CLI, поэтому автоматические проверки не могут подтверждать его
совместимость.

## Решение

`cmd/stepan` выбирает provider только явными flags:

```text
--agent codex|claude|qwen
--agent-cli-name <name>
```

Без flags сохраняется Codex из `PATH`. Для Claude flag executable опционален:
официальное имя `claude` либо exact supplied simple name разрешается через
`PATH`, затем Stepan передаёт найденный абсолютный regular-file path в SDK и не
вызывает `--version` как vendor gate. Пути, directory separators и явно пустое
значение отклоняются без fallback.

Введён минимальный внутренний контракт `internal/agentruntime`:

```text
cmd/stepan → specflow → agentruntime ← codexapp
                                      ← claudeapp
```

`specflow` хранит только opaque thread handle и неизменную конфигурацию thread:
bootstrap, schema, read-only workspace и один внешний writable artifact root. Codex
сохраняет App Server protocol и process containment через Job Object/process
group. Claude использует `github.com/severity1/claude-agent-sdk-go v0.6.22` и
его штатный subprocess transport без fork, launcher shim или custom transport.

Claude SDK получает ровно `Read`, `Write`, `Edit`, `Glob`, `Grep`, default
permission mode, только user setting source и выключенные skills. Это позволяет
CLI использовать доверенные credentials, сессии и оперативные данные из
`~/.claude`; project/local setting sources не загружаются. MCP, Bash, hooks,
plugins, agents, additional directories и sandbox auto-allow не настраиваются
Stepan.
Thread-safe callback дополнительно проверяет workspace и turn-scoped write root.
Внешние инструменты в эту версию не входят.

SDK output schema задаётся при создании Client, поэтому ему передаётся закрытый
union всех `/feature` terminal envelopes. `specflow` всё равно валидирует более
узкую schema текущего этапа и не меняет state от свободного текста.
Transport-копия union получает явный верхнеуровневый `type: object` и не
содержит `$schema` dialect annotations для совместимости с custom-tool schema
корпоративных API; доменная schema при этом не изменяется.

## Принятый риск

Для Claude v1 Stepan не контролирует дерево процессов. `Disconnect` и
best-effort bounded `Interrupt` должны закрыть прямой CLI process. Риск принят,
поскольку Claude не получает shell, MCP, hooks, plugins, background tasks или
предусмотренный протокол внешних инструментов. Ручной сценарий `CLAUDE-M03`
обязан подтвердить, что конкретный corporate build не оставляет процессы и не
продолжает записывать workspace; до этого поддержка такого build имеет статус
`BLOCKED`.

## Последствия

- Codex остаётся default и сохраняет containment.
- Совместимость corporate fork устанавливается protocol/tool conformance, а не
  строкой версии.
- SDK upgrade или новый corporate build требует повторения fake regression suite
  и всех `CLAUDE-M01`–`CLAUDE-M03` на заявленной платформе.
- Возврат к process-tree containment Claude потребует отдельного ADR, а не
  скрытого изменения transport.

## Связанные материалы

- [Спецификация Claude CLI](../changes/features/claude-cli-support/specification.md)
- [План ручной приёмки](../changes/features/claude-cli-support/manual-test-plan.md)
- [ADR 0001](0001-codex-app-server-containment.md)
- [ADR 0002](0002-current-stack-and-architecture.md)
