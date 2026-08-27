# План исправления P1-замечаний Claude CLI

Статус: готов к реализации  
Основание: ревью реализации
[`implementation-plan.md`](implementation-plan.md)  
Область: только четыре замечания приоритета P1

## 1. Правила выполнения

- Каждая задача ниже выполняется отдельным commit вместе со своими автотестами.
- Следующая зависимая задача начинается только после прохождения автотестов
  предыдущей.
- Проверки используют только unit tests и fake integration tests проекта. Они
  не запускают настоящий Claude CLI, не требуют сети, credentials или
  корпоративного окружения.
- В этот план не входят live-приёмка, ручные сценарии, сборочные проверки,
  `go vet`, `actionlint` и другие проверки за пределами автотестов.
- Исправления не должны ослаблять Codex runtime, общий `/idea` flow, tool
  allowlist или Git post-check.

## 2. Порядок работ

| ID | Результат | Зависит от |
|---|---|---|
| P1-01 | Claude CLI получает действительно пустой набор setting sources | — |
| P1-02 | Относительные пути файловых tools проверяются от workspace | — |
| P1-03 | Запуск runtime отменяем и очищает partial initialization | P1-01 |
| P1-04 | Поток ответов строго изолирован по turn/session | P1-03 |

## 3. Задачи

### P1-01. Зафиксировать пустой набор Claude setting sources

**Проблема:** `WithSettingSources()` без аргументов записывает `nil`. В
закреплённом SDK сочетание `SettingSources == nil` и непустого значения поля
`Skills` включает источники `user,project` при построении CLI arguments, даже
если skills заданы пустым списком.

**Результат:** effective SDK options однозначно отключают user, project и local
setting sources. Конфигурация не зависит от SDK defaults и не позволяет вернуть
hooks, MCP, plugins или дополнительные tools через локальные настройки.

**Изменения:**

- Передавать в `WithSettingSources` non-nil slice нулевой
  длины либо применить эквивалентный способ, при котором итоговое поле
  `SettingSources` не равно `nil` и имеет длину `0`.
- Создавать проверяемые options тем же публичным `claudecode.NewOptions`, который
  использует production factory. Не эмулировать применение option-функций на
  вручную созданном `&claudecode.Options{}`.
- Сохранить `WithSkillsDisabled`, точный набор пяти tools, пустые MCP/plugins/
  agents/additional directories и `PermissionModeDefault`.

**Критерии приёмки автотестами:**

- Тест production options builder проверяет одновременно
  `SettingSources != nil` и `len(SettingSources) == 0`.
- Тест строит options через `claudecode.NewOptions(claudeOptions(...)...)` и
  поэтому воспроизводит реальные SDK defaults.
- Тест подтверждает, что `Skills` является non-nil пустым
  списком, а не `nil` и не значением `all`.
- Тест подтверждает точный порядок и состав `Read`, `Write`, `Edit`, `Glob`,
  `Grep` и отсутствие MCP, hooks, plugins, agents и additional directories.
- Regression case с прежним `nil`-значением setting sources должен падать на
  новой проверке.

**Автоматическая проверка:**

```text
go test ./internal/agentruntime/claudeapp
```

### P1-02. Исправить разрешение относительных путей write-tools

**Проблема:** `Write` и `Edit` сейчас передают относительный path в
`resolvePath(writableRoot, path)`. Permission callback проверяет один абсолютный
путь, а CLI с `WithCwd(workspace)` может выполнить операцию над другим путём,
разрешённым относительно workspace.

**Результат:** любой model-supplied path сначала однозначно разрешается
относительно canonical workspace. Только полученный абсолютный canonical
candidate сравнивается с workspace и, для `Write`/`Edit`, с единственным
writable root.

**Изменения:**

- Разделить операции «разрешить supplied path относительно workspace» и
  «проверить candidate внутри allowed root».
- Для относительного path всегда использовать canonical workspace как base,
  независимо от типа tool и активной policy.
- Канонизировать существующий target или ближайшего существующего ancestor для
  нового файла до containment check.
- Валидировать и хранить canonical writable root до установки active policy.
- Сохранить fail-closed поведение для symlink, junction, reparse point, другого
  volume, `..`, отсутствующего/wrong-type path и неизвестного tool.
- Отклонять неоднозначный payload, если он содержит несколько конкурирующих
  path-представлений.

**Критерии приёмки автотестами:**

- В write-turn относительный `specification.md` отклоняется, если writable root
  равен `<workspace>/docs/specs/example`.
- Относительный `docs/specs/example/specification.md` и соответствующий
  абсолютный path разрешаются.
- `../`, абсолютный path вне workspace и абсолютный path внутри workspace, но
  вне writable root, отклоняются.
- Новый файл внутри writable root разрешается при безопасном существующем
  ancestor.
- `Read`, `Glob` и `Grep` используют workspace как base и не читают за его
  пределами.
- `Write` и `Edit` в read-only turn всегда отклоняются.
- Для всех пяти tools покрыты missing path, wrong type, explicit `null`,
  неоднозначный payload и неизвестный tool.
- Platform-specific автотесты покрывают symlink на POSIX и доступные
  junction/reparse/case-insensitive сценарии на Windows.
- Fake callback test проверяет, что разрешённый callback path совпадает с тем
  абсолютным path, который должен использовать CLI при `WithCwd(workspace)`.

**Автоматическая проверка:**

```text
go test ./internal/agentruntime/claudeapp
go test -count=20 ./internal/agentruntime/claudeapp
go test -race ./internal/agentruntime/claudeapp
```

### P1-03. Сделать запуск runtime отменяемым и очистить partial Connect

**Проблема:** `Session.current` удерживает mutex во время блокирующего factory
call, а Claude `Connect` получает `context.Background()`. `Interrupt` не может
отменить зависший startup. Ошибка после частичной инициализации Client также не
проходит через гарантированный cleanup.

**Результат:** startup имеет собственный lifecycle context, не выполняется под
mutex и имеет один общий terminal path. Отмена до или во время `Connect`
ограниченно завершает startup, а частично созданный Client очищается ровно один
раз.

**Изменения:**

- Передавать `context.Context` через runtime factory до `StartRuntime` и SDK
  `Connect`; не использовать `context.Background()` для startup.
- Представить незавершённый запуск отдельным session state/attempt с `cancel` и
  `done`.
- Под mutex только публиковать или читать состояние attempt/runtime; factory и
  SDK calls выполнять без session mutex.
- `Interrupt` должен помечать Session остановленной, отменять startup и ждать
  его общего terminal path, не ожидая mutex, удерживаемый `Connect`.
- При ошибке или отмене `Connect` отменять lifecycle context, вызывать доступный
  cleanup Client и объединять исходную и cleanup errors через `errors.Join`.
- Runtime, завершивший startup после остановки Session, не публиковать: сразу
  закрыть его и вернуть provider-neutral cancellation/closed error.
- Сохранить lazy start и переиспользование одного успешно подключённого runtime
  между последовательными flows.

**Критерии приёмки автотестами:**

- Cancel до startup не вызывает client factory и возвращает
  `ErrRuntimeClosed`/cancellation согласно выбранному контракту.
- Fake `Connect`, ожидающий `ctx.Done()`, завершается после `Session.Interrupt`
  без deadlock; тест имеет короткий собственный timeout.
- Interrupt во время startup вызывает cancel один раз и не публикует runtime.
- Fake Client, вернувший ошибку после partial initialization, получает ровно
  один cleanup/`Disconnect`.
- Если одновременно ошибаются `Connect` и cleanup, возвращаемая ошибка
  распознаёт обе причины через `errors.Is`.
- Late-success race закрывает созданный runtime и не позволяет последующему
  `StartThread` использовать его.
- Повторные `Interrupt` и `Close` во время startup идемпотентны и ждут один
  terminal path.
- Успешный lazy startup по-прежнему происходит один раз, а два последовательных
  flow получают разные thread handles одного runtime.
- Повторный и race-запуск тестов не обнаруживает зависших goroutines или data
  races.

**Автоматическая проверка:**

```text
go test ./internal/agentruntime/claudeapp ./internal/specflow ./cmd/stepan
go test -count=20 ./internal/agentruntime/claudeapp ./internal/specflow ./cmd/stepan
go test -race ./internal/agentruntime/claudeapp ./internal/specflow
```

### P1-04. Ввести строгую границу response stream для каждого turn

**Проблема:** collector возвращает первый `ResultMessage` и закрывает только
локальный iterator. Дополнительный terminal result остаётся в общем SDK stream
и может быть принят следующим turn или другой session.

**Результат:** один runtime-owned receiver последовательно читает SDK stream,
сопоставляет сообщения с активным query/session и не позволяет stale,
duplicate или conflicting terminal result попасть в следующий ход.

**Изменения:**

- Явно представить активный query и его ожидаемый session ID во внутреннем
  состоянии Claude runtime.
- Использовать один контролируемый receive path на Client вместо независимых
  короткоживущих collectors над общим message channel.
- Проверять session ID terminal result до публикации результата вызывающему
  `RunTurn`.
- Первый корректный terminal result завершает логический query. Любое следующее
  сообщение до регистрации нового query считается stale protocol data;
  duplicate/conflicting result переводит runtime в unhealthy state и никогда
  не используется как ответ следующего turn.
- Если duplicate обнаружен до завершения текущего `RunTurn`, вернуть ошибку
  текущего хода. Если он поступил после публикации terminal result, запретить
  следующий SDK query и вернуть `ErrRuntimeExited` до его отправки.
- На `Interrupt`/`Close` завершать receiver и всех ожидающих callers через общий
  terminal path.
- Сохранить строгую проверку `IsError`, наличия structured output и типа JSON
  object; не добавлять fallback из assistant text или Markdown.

**Критерии приёмки автотестами:**

- Assistant/thinking/tool messages перед terminal result игнорируются как
  output, а один корректный result возвращает defensive-copy JSON object.
- Result с чужим или пустым session ID отклоняется до изменения flow state.
- Два terminal results одного query не дают два успешных ответа.
- Duplicate, уже находящийся в fake stream, завершает текущий ход ошибкой.
- Delayed duplicate после первого terminal result делает runtime unhealthy;
  следующий `RunTurn` возвращает `ErrRuntimeExited`, не вызывая
  `QueryWithSession`.
- Два последовательных корректных turns одного handle и turns двух разных
  handles получают только свои результаты.
- SDK error result, `nil`, `null`, scalar, array, premature EOF, iterator error
  и context cancellation завершаются ошибкой.
- После `Interrupt`/`Close` fake receiver завершается, iterator закрывается
  ровно один раз, ожидающие goroutines не остаются.
- Повторный и race-запуск тестов не обнаруживает утечки сообщений, goroutines
  или data races.

**Автоматическая проверка:**

```text
go test ./internal/agentruntime/claudeapp ./internal/specflow
go test -count=20 ./internal/agentruntime/claudeapp ./internal/specflow
go test -race ./internal/agentruntime/claudeapp ./internal/specflow
```

## 4. Итоговый автоматический gate

После выполнения P1-01–P1-04 запускаются только автотесты проекта:

```text
go test ./...
go test -count=20 ./internal/agentruntime/claudeapp ./internal/specflow ./cmd/stepan
go test -race ./internal/agentruntime/claudeapp ./internal/specflow
```

P1-исправления считаются принятыми, если все перечисленные команды завершились
успешно и каждый критерий выше проверяется отдельным тестовым case либо явно
названным table-driven case.
