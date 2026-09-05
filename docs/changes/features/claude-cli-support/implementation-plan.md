# Stepan: задачи поддержки Claude Code-совместимого CLI

Статус: черновик для ревью  
Основание: [спецификация](specification.md)  
Целевая среда: Windows native/amd64 и macOS native/arm64  
Live-среда: корпоративный Claude Code-совместимый CLI под simple name в `PATH`

## 1. Правила декомпозиции

- Одна задача ниже — один безопасный commit. После каждого commit проект
  собирается, а относящиеся к нему тесты проходят без настоящего Claude.
- Production-код и его unit/integration tests входят в одну задачу. Задач
  «добавить тесты потом» нет.
- Следующая задача начинается после выполнения критериев всех её зависимостей.
- Сначала выделяется контракт из двух фактических реализаций текущих операций,
  а не создаётся общий provider framework, registry или DSL.
- До подключения Claude все Codex tests обязаны остаться зелёными; временное
  ослабление Codex containment, approvals или schema checks запрещено.
- SDK types не выходят из `internal/agentruntime/claudeapp`; `specflow` знает только
  provider-neutral contract.
- Exact SDK version закрепляется в `go.mod`. Диапазон версий, floating branch и
  автоматическое обновление запрещены.
- Автоматические тесты не требуют установленного Claude, корпоративной сети,
  credentials или изменения пользовательского Claude profile.
- Live-приёмка выполняется отдельно и может оставаться `BLOCKED` на машине
  разработки. До её `PASS` нельзя заявлять поддержку конкретного форка.
- В первой версии не добавляются MCP, shell, hooks, plugins, external tool
  protocol, custom transport, launcher shim или Claude process containment.

## 2. Порядок выполнения

| ID | Результат | Зависит от |
|---|---|---|
| CC-01 | Provider-neutral runtime contract и policy | — |
| CC-02 | Codex реализует новый contract без регрессии | CC-01 |
| CC-03 | `specflow` полностью отвязан от `codexapp` | CC-02 |
| CC-04 | Явный выбор provider и PATH-name executable | CC-03 |
| CC-05 | Закреплён SDK и введён тестируемый Claude client seam | CC-01 |
| CC-06 | Общий flow envelope schema для Claude | CC-03, CC-05 |
| CC-07 | Безопасная конфигурация SDK и corporate CLI preflight | CC-04–CC-06 |
| CC-08 | Permission evaluator для пяти файловых tools | CC-01, CC-07 |
| CC-09 | Строгий collector terminal structured output | CC-05, CC-06 |
| CC-10 | Claude runtime, sessions и последовательные turns | CC-08, CC-09 |
| CC-11 | Composition root и полный `/idea` через Claude | CC-04, CC-10 |
| CC-12 | Interrupt, close, crash recovery и диагностика | CC-10, CC-11 |
| CC-13 | Provider conformance и сквозные fake-сценарии | CC-02, CC-11, CC-12 |
| CC-14 | ADR, platform build gates и подготовка live-приёмки | CC-13 |

## 3. Commit-задачи

### CC-01. Provider-neutral runtime contract и policy

**Результат:** новый внутренний пакет описывает только session/thread, turn,
policy и lifecycle, которые уже нужны `specflow`.

**Граница commit:** новый `internal/agentruntime` и его unit tests. Production
пакеты пока не мигрируют.

**Критерии приёмки:**

- Contract не импортирует `codexapp`, Claude SDK, UI или Git implementation.
- Thread handle нельзя случайно использовать с другим runtime.
- `TurnOptions` содержит prompt-independent output schema и immutable policy.
- Read-only policy всегда имеет workspace как readable root и не имеет
  writable roots.
- Single-write-root policy принимает ровно один абсолютный canonical root
  внутри workspace.
- Пустой workspace, относительный путь, выход через `..`, symlink, junction или
  reparse point отклоняются.
- Определены provider-neutral errors: closed runtime, interrupted turn,
  concurrent turn и unexpected runtime exit.

**Чек-лист реализации:**

- [ ] Создать минимальные `Runtime`, `Thread`, `TurnOptions` и `TurnPolicy`.
- [ ] Сделать thread ownership проверяемым runtime, не раскрывая provider state
  контроллеру.
- [ ] Перенести семантику `ReadOnlyTurnPolicy` и
  `SingleWriteRootTurnPolicy` в provider-neutral package.
- [ ] Не включать model, token budget, MCP, commands, network или provider name
  в contract.
- [ ] Сделать schema defensive copy, чтобы вызывающий код не менял активный
  запрос через общий slice.
- [ ] Покрыть policy table tests на Windows path semantics и POSIX paths через
  доступные build-specific tests.
- [ ] Проверить повторное использование и foreign thread handle.

**Проверка:** `go test ./internal/agentruntime` и `go test ./...`.

### CC-02. Codex реализует новый contract без регрессии

**Результат:** `codexapp.Runtime` реализует `agentruntime.Runtime`, сохраняя
App Server protocol, exact version pin, approvals и process containment.

**Граница commit:** `internal/agentruntime/codexapp`, его tests и минимальные совместимые
aliases только там, где они нужны для безопасной миграции.

**Критерии приёмки:**

- Наблюдаемые App Server requests и policies не меняются.
- Каждый Codex thread принадлежит создавшему его runtime.
- Read-only и write-turn по-прежнему используют read-only sandbox без сети;
  запись разрешается только approval для единственного root.
- `Interrupt` сохраняет grace period и закрытие Job Object/process group.
- Provider-specific errors корректно wrap provider-neutral sentinels.
- Probe executables продолжают собираться и работать по прежнему API либо
  получают локальную адаптацию без изменения evidence contract.

**Чек-лист реализации:**

- [ ] Заменить собственные `Thread`, `TurnOptions`, `TurnPolicy` production-типы
  contract-типами или явным adapter layer.
- [ ] Не переносить JSON-RPC message types в `agentruntime`.
- [ ] Сохранить проверку output schema до `turn/start`.
- [ ] Сохранить `approvalPolicy=on-request`, read-only sandbox и
  `networkAccess=false`.
- [ ] Сохранить correlation, duplicate terminal checks и strict JSON object
  decoder.
- [ ] Обновить compile-time interface assertion.
- [ ] Запустить lifecycle tests повторно для выявления race.

**Проверка:** `go test ./internal/agentruntime/codexapp`, `go test -count=10
./internal/agentruntime/codexapp` и `go test ./...`.

### CC-03. `specflow` полностью отвязан от `codexapp`

**Результат:** controller и lazy `Session` используют только
`internal/agentruntime`; текущий Codex `/idea` остаётся эталонным поведением.

**Граница commit:** `internal/specflow`, его tests и минимальная wiring-правка
`cmd/stepan` для прежнего Codex default.

**Критерии приёмки:**

- `rg "internal/agentruntime/codexapp" internal/specflow` не находит imports.
- Controller не проверяет provider name и не ветвится по Codex/Claude.
- Lazy start, reuse между flows, discard после runtime error и final close
  сохраняются.
- Все stage schemas, prompts, Git snapshots и write postconditions не меняются.
- Существующий запуск `stepan` без аргументов по-прежнему использует Codex.

**Чек-лист реализации:**

- [ ] Перевести `initialTurnRunner`, controller thread и options на contract.
- [ ] Заменить Codex-specific `NewSession(executable, workspace)` на lazy
  runtime factory; оставить удобный Codex constructor только в composition root.
- [ ] Перевести fake runtimes и acceptance fixtures на provider-neutral types.
- [ ] Сохранить сброс runtime только для lifecycle/protocol error, а flow — для
  stage decode/postcondition error.
- [ ] Проверить отсутствие provider-specific текста в прикладных ошибках, кроме
  диагностической cause.
- [ ] Не создавать registry providers в `specflow`.

**Проверка:** `go test ./internal/specflow ./internal/agentruntime/codexapp ./cmd/stepan` и
`go test ./...`.

### CC-04. Явный выбор provider и PATH-name executable

**Результат:** `cmd/stepan` принимает `--agent` и optional `--agent-cli-name`,
однозначно валидирует конфигурацию и сохраняет Codex default.

**Граница commit:** небольшой config package либо testable parsing function в
`cmd/stepan`, tests и usage text. Claude runtime ещё не запускается.

**Критерии приёмки:**

- Без flags получается `{agent: codex, executable: codex}`.
- `--agent claude` без `--agent-cli-name` выбирает `claude` из `PATH`.
- Явный executable должен быть simple non-empty name без directory separators.
- Явное имя разрешается через `PATH` и остаётся авторитетным без fallback.
- Неизвестный agent и лишние positional arguments отклоняются.
- Ошибка config происходит до поиска Git root, запуска UI и запуска агента.

**Чек-лист реализации:**

- [ ] Использовать стандартный `flag.FlagSet` с контролируемым writer, а не
  добавлять CLI framework.
- [ ] Ввести закрытый enum `codex|claude`.
- [ ] Отделить parsing/name validation от PATH resolution для table tests.
- [ ] Отклонять absolute path и directory separators; PATH result проверять как
  regular file и не выполнять CLI `--version` как vendor gate.
- [ ] Не вводить environment/project config fallback.
- [ ] Обновить help с примером корпоративного пути Windows и macOS.
- [ ] Проверить, что invalid flags не запускают lazy runtime.

**Проверка:** `go test ./cmd/stepan` и `go test ./...`.

### CC-05. Закреплён SDK и введён тестируемый Claude client seam

**Результат:** exact версия `claude-agent-sdk-go` добавлена в модуль, а
`internal/agentruntime/claudeapp` изолирует минимальную часть Client API за локальным
интерфейсом.

**Граница commit:** `go.mod`, `go.sum`, skeleton `internal/agentruntime/claudeapp`, SDK API
contract tests. Runtime поведения ещё нет.

**Критерии приёмки:**

- В `go.mod` записана точная tagged версия, выбранная после просмотра changelog
  и публичного API; используется стандартная Go checksum verification.
- SDK imports существуют только внутри `internal/agentruntime/claudeapp` и его tests.
- Локальный seam содержит лишь `Connect`, `Disconnect`, `QueryWithSession`,
  `ReceiveResponse`/эквивалент и `Interrupt`.
- Factory SDK client можно заменить fake без процесса или сети.
- Обновление зависимости не может произойти неявно через range.

**Чек-лист реализации:**

- [ ] Зафиксировать в комментарии package выбранную SDK version и ссылку на
  проверенный upstream commit/tag.
- [ ] Проверить MIT license и транзитивные зависимости.
- [ ] Описать минимальные локальные message/result representations либо mapper,
  не экспортируя SDK aliases наружу.
- [ ] Добавить compile-time assertions против публичного SDK API.
- [ ] Добавить dependency injection только в package-private constructor.
- [ ] Выполнить `go mod tidy` и проверить неожиданный dependency growth.

**Проверка:** `go mod tidy`, `go test ./internal/agentruntime/claudeapp` и `go test ./...`.

### CC-06. Общий flow envelope schema для Claude

**Результат:** `specflow` предоставляет закрытую union schema всех terminal
envelopes текущего `/idea`, пригодную для настройки долгоживущего SDK Client.

**Граница commit:** schema builder/tests в `internal/specflow`; adapter пока не
выполняет turns.

**Критерии приёмки:**

- Union принимает все валидные результаты `Initial`, `Create`, `Question`,
  `Change` и `Update`.
- Union отклоняет неизвестный status, лишние поля, отсутствующие conditional
  поля, массив, scalar и `null`.
- Stage schemas и strict decoders остаются окончательной проверкой конкретного
  хода.
- Schema детерминирована: повторная генерация даёт те же bytes/hash.
- Конвертация в `map[string]any` для `WithJSONSchema` проверяет object и не
  теряет числа/boolean.

**Чек-лист реализации:**

- [ ] Собрать envelope из одного `oneOf` с `additionalProperties=false` в
  каждой ветви.
- [ ] Не ослаблять существующие stage schemas ради объединения.
- [ ] Добавить positive cases из текущих controller tests.
- [ ] Добавить cross-stage negative cases, например `WRITTEN` на initial stage.
- [ ] Проверить schema библиотекой/validator, уже выбранной проектом; если её
  нет, тестировать структуру и stage decoders без новой тяжёлой зависимости.
- [ ] Передавать envelope в runtime factory из composition root, чтобы
  `claudeapp` не импортировал `specflow`.

**Проверка:** `go test ./internal/specflow` и `go test ./...`.

### CC-07. Безопасная конфигурация SDK и corporate CLI preflight

**Результат:** один options builder создаёт закрытую Claude-конфигурацию из
validated executable, workspace и envelope schema.

**Граница commit:** `internal/agentruntime/claudeapp` config/options и tests через
`claudecode.NewOptions`; процесс ещё можно не запускать.

**Критерии приёмки:**

- `WithCLIPath` получает именно разрешённый validated абсолютный путь.
- `WithCwd` получает canonical Git root; additional directories отсутствуют.
- `WithTools` содержит ровно `Read`, `Write`, `Edit`, `Glob`, `Grep`.
- Full Claude Code preset, allowed-all и bypass permission mode отсутствуют.
- MCP maps, hooks, plugins и agents пусты; skills отключены.
- User/project/local setting sources не подключены.
- Background tasks и agent view отключаются зафиксированными environment flags.
- Envelope передаётся через `WithJSONSchema`.

**Чек-лист реализации:**

- [ ] Создать immutable `Config` с executable, workspace, envelope schema и
  диагностическим stderr limit.
- [ ] Повторно проверить simple executable name, разрешить его через `PATH` и
  проверить regular-file result на package boundary; не искать fallback.
- [ ] Канонизировать workspace и проверить Git root до `Connect`.
- [ ] Настроить `PermissionModeDefault` и `WithCanUseTool`.
- [ ] Вызвать `WithSettingSources()` с пустым набором и
  `WithSkillsDisabled()`; не полагаться на SDK defaults.
- [ ] Не передавать `WithMcpServers`, `WithHooks`, `WithPlugins`,
  `WithAgents`, sandbox auto-allow или `WithAddDirs`.
- [ ] Использовать bounded stderr callback/writer без записи prompts и env.
- [ ] Проверить созданный `Options` field-by-field в test.

**Проверка:** `go test ./internal/agentruntime/claudeapp` и `go test ./...`.

### CC-08. Permission evaluator для пяти файловых tools

**Результат:** thread-safe callback разрешает только файловую операцию,
допустимую активной turn policy.

**Граница commit:** `internal/agentruntime/claudeapp` permission/path evaluator и tests;
при необходимости маленький общий path helper без SDK imports.

**Критерии приёмки:**

- Вне активного turn callback работает fail-closed.
- `Read`, `Glob`, `Grep` не получают доступ вне workspace.
- `Write`, `Edit` всегда отклоняются в read-only turn.
- В write-turn они допускаются только внутри writable root.
- Относительные paths разрешаются только относительно canonical workspace.
- Новый файл внутри root допустим при безопасном существующем ancestor.
- Symlink/junction/reparse escape отклоняется.
- Missing/wrong-type path field, unknown tool и неоднозначный payload
  отклоняются с безопасной причиной.
- Allow result не содержит permission updates, переживающих текущий вызов.

**Чек-лист реализации:**

- [ ] Описать ожидаемые path fields для `Read`, `Write`, `Edit`, `Glob`, `Grep`;
  отсутствие path у search tool трактовать как workspace только если это
  соответствует закреплённому CLI protocol.
- [ ] Нормализовать path без shell expansion, glob expansion или доверия к
  model-provided cwd.
- [ ] Для будущего файла канонизировать ближайшего существующего ancestor.
- [ ] Переиспользовать проверенные containment primitives, не импортируя
  `codexapp` из `claudeapp`.
- [ ] Хранить active policy под mutex и очищать её через `defer` после хода.
- [ ] Добавить race test конкурентного callback с cancel/close.
- [ ] Проверить case-insensitive Windows containment и volume mismatch.

**Проверка:** `go test ./internal/agentruntime/claudeapp`, `go test -race
./internal/agentruntime/claudeapp` на поддерживаемой машине и `go test ./...`.

### CC-09. Строгий collector terminal structured output

**Результат:** adapter превращает поток SDK messages одного query в ровно один
provider-neutral JSON object либо классифицированную ошибку.

**Граница commit:** `internal/agentruntime/claudeapp` message collector/decoder и fake tests.

**Критерии приёмки:**

- Assistant text/thinking/tool events не принимаются как terminal output.
- Один успешный terminal result с object возвращает defensive copy JSON bytes.
- SDK error result, iterator error и premature EOF возвращают ошибку.
- `nil`, `null`, scalar, array, trailing data и пустой object при несовместимой
  schema отклоняются.
- Duplicate/conflicting terminal result отклоняется.
- Context cancellation не маскируется как успешный EOF.
- Никакой fallback extraction из Markdown или свободного текста нет.

**Чек-лист реализации:**

- [ ] Адаптировать SDK `ResultMessage` в package-private terminal record.
- [ ] Проверить `IsError`, subtype/stop reason и structured output field по
  фактической закреплённой SDK version.
- [ ] Ограничить объём сохраняемой диагностики и число buffered messages.
- [ ] После terminal result корректно завершить iterator/receive path.
- [ ] Возвращать provider-neutral protocol/runtime errors с безопасной cause.
- [ ] Добавить fixtures: success, null structured output, error result,
  duplicate result, disconnect и cancel.

**Проверка:** `go test ./internal/agentruntime/claudeapp` и `go test ./...`.

### CC-10. Claude runtime, sessions и последовательные turns

**Результат:** `claudeapp.Runtime` реализует полный `agentruntime.Runtime` на
одном SDK Client.

**Граница commit:** runtime implementation и unit/fake integration tests внутри
`internal/agentruntime/claudeapp`.

**Критерии приёмки:**

- `StartRuntime` создаёт Client с безопасными options и вызывает `Connect` один
  раз.
- `StartThread` возвращает новый runtime-owned session ID без обращения к
  глобальному списку Claude sessions.
- `RunTurn` использует `QueryWithSession` и соответствующий handle.
- Ходы одного handle видят общий контекст; разные handles изолированы.
- Foreign/nil handle и пустой prompt отклоняются до SDK call.
- Одновременный второй ход получает `ErrTurnInProgress`.
- Active permission policy устанавливается до query и очищается на всех exit
  paths.
- Stage output schema валидируется как JSON object до query, хотя SDK Client
  уже настроен общим envelope.
- Protocol/connection failure помечает runtime unhealthy; stage-level invalid
  envelope не обязан закрывать здоровую connection.

**Чек-лист реализации:**

- [ ] Реализовать явную state machine `new/connected/turn/closing/closed`.
- [ ] Генерировать session ID без user data и provider branding.
- [ ] Не вызывать SDK session listing/resume APIs.
- [ ] Сериализовать `RunTurn` через mutex/try-lock без deadlock callback.
- [ ] После `QueryWithSession` синхронно собрать response до terminal result.
- [ ] Классифицировать SDK not-found, connection, process и decode errors.
- [ ] Добавить compile-time assertion `agentruntime.Runtime`.
- [ ] Проверить close до connect failure и partial initialization.

**Проверка:** `go test ./internal/agentruntime/claudeapp`, `go test -count=20
./internal/agentruntime/claudeapp` и `go test ./...`.

### CC-11. Composition root и полный `/idea` через Claude

**Результат:** `cmd/stepan --agent claude [--agent-cli-name <name>]` лениво
создаёт Claude runtime и проводит существующий flow без provider branches в
controller.

**Граница commit:** `cmd/stepan`, provider factory wiring, `internal/specflow`
integration fixtures. Реальный CLI не нужен.

**Критерии приёмки:**

- До первой `/idea` SDK Client/executable не запускается.
- Claude factory получает canonical Git root, validated CLI path и общий
  envelope schema.
- Initial clarification, create, question, change и approve используют один
  Claude session handle.
- Следующий `/idea` получает новый handle той же runtime connection.
- Git snapshot, target containment и mandatory `specification.md` одинаковы для
  Codex и Claude.
- UI тексты не обещают Codex там, где выбран Claude; workflow команды не
  меняются.
- Default `stepan` остаётся Codex-compatible.

**Чек-лист реализации:**

- [ ] Построить provider factory только в composition root через закрытый
  `switch` по enum.
- [ ] Передать factory в lazy `specflow.Session`.
- [ ] Не создавать global singleton или provider registry.
- [ ] Прокинуть envelope только Claude factory; Codex сохраняет per-turn schema.
- [ ] Добавить fake Claude factory и полный controller acceptance path.
- [ ] Проверить два последовательных flows и отсутствие context leakage.
- [ ] Проверить invalid Claude config до TTY loop и runtime start.

**Проверка:** `go test ./cmd/stepan ./internal/specflow ./internal/agentruntime/claudeapp` и
`go test ./...`.

### CC-12. Interrupt, close, crash recovery и диагностика

**Результат:** все terminal paths Claude adapter bounded, идемпотентны и
согласованы с exit semantics Stepan.

**Граница commit:** `internal/agentruntime/claudeapp`, `internal/specflow`, `cmd/stepan` и
lifecycle tests.

**Критерии приёмки:**

- Normal application exit вызывает `Disconnect` один раз.
- `Ctrl+C` во время turn best-effort вызывает SDK `Interrupt`, ограниченно ждёт
  и затем вызывает `Disconnect`.
- `Ctrl+C` вне turn закрывает Client без лишнего query.
- Повторные `Interrupt`/`Close` не паникуют и ждут общий terminal path.
- Stepan возвращает code `130` для operator cancellation и `2` для runtime
  failure.
- CLI crash сбрасывает текущий runtime; следующий `/idea` создаёт новый Client.
- Bounded stderr добавляется к runtime error, но не к успешному результату.
- Adapter не заявляет и не симулирует process-tree containment.

**Чек-лист реализации:**

- [ ] Добавить `sync.Once`/state synchronization для close.
- [ ] Не удерживать runtime mutex во время blocking SDK calls.
- [ ] Использовать отдельный bounded context для `Interrupt`; после grace всегда
  продолжать `Disconnect`.
- [ ] Объединять cleanup errors, не теряя исходную причину turn failure.
- [ ] Проверить cancel до Connect, во время Connect, во время Query и после
  terminal result fake-сценариями.
- [ ] Проверить отсутствие goroutine/channel leaks через повторные tests.
- [ ] Не обращаться к OS process enumeration из production-кода первой версии.

**Проверка:** `go test -count=20 ./internal/agentruntime/claudeapp ./internal/specflow
./cmd/stepan`, затем `go test ./...`.

### CC-13. Provider conformance и сквозные fake-сценарии

**Результат:** общий test suite доказывает одинаковый прикладной contract Codex
и Claude без live executables.

**Граница commit:** test harness и tests; production API не расширяется ради
удобства теста.

**Критерии приёмки:**

- Один набор provider-neutral сценариев выполняется для Codex fake и Claude
  fake.
- Покрыты initial clarification, create, read-only question, clarified update,
  approve и второй независимый flow.
- Покрыты invalid schema, wrong-stage status, missing entrypoint, write escape,
  runtime crash и cancel.
- Claude-specific suite доказывает exact tools/options, permission callback,
  session routing и terminal collector.
- Codex-specific suite сохраняет App Server protocol и process containment.
- Suite не требует сети, CLI, auth, user settings или изменения настоящего Git
  index.

**Чек-лист реализации:**

- [ ] Выделить conformance test function вокруг `agentruntime.Runtime`, а не
  production registry.
- [ ] Не заставлять Codex fake имитировать Claude messages и наоборот.
- [ ] Использовать temp Git repos и synthetic dirty baseline.
- [ ] Проверять фактический Git diff независимо от fake terminal result.
- [ ] Добавить regression, где fake `claude` в `PATH` не выбран при явном
  corporate PATH-name.
- [ ] Запустить lifecycle packages с `-count=20` и `-race` там, где доступно.
- [ ] Проверить `go vet` и `git diff --check`.

**Проверка:** `go test ./...`, `go test -count=20 ./internal/agentruntime
./internal/agentruntime/claudeapp ./internal/specflow`, `go vet ./...`,
`git diff --check`.

### CC-14. ADR, platform build gates и подготовка live-приёмки

**Результат:** архитектурный baseline и пользовательская документация отражают
двух provider, а обе поддерживаемые сборки компилируются до ручной приёмки.

**Граница commit:** `docs`, build/workflow adjustments при необходимости и
только исправления portability, обнаруженные cross-build.

**Критерии приёмки:**

- Новый ADR фиксирует agent runtime boundary, неофициальный SDK, tool model и
  осознанное отсутствие Claude process containment.
- ADR 0002 обновлён или помечен superseded в изменившихся разделах.
- README/help описывает `--agent claude [--agent-cli-name <name>]` и статус
  ручной совместимости.
- Windows/amd64 build и tests проходят с Go `1.26.5`.
- Darwin/arm64 cross-build проходит с `CGO_ENABLED=0`.
- Manual plan содержит фактические имена flags, команды наблюдения и поля
  evidence после реализации.
- Невыполненная live-приёмка явно имеет статус `BLOCKED`, а не `PASS`.

**Чек-лист реализации:**

- [ ] Создать следующий ADR в `docs/adr/` и связать спецификацию.
- [ ] Обновить архитектурную диаграмму `cmd/stepan → agentruntime →
  codexapp|claudeapp`.
- [ ] Зафиксировать exact SDK version и corporate CLI version matrix template.
- [ ] Обновить manual plan реальными командами Windows/macOS и CLI flags.
- [ ] Выполнить поддерживаемую Windows сборку и tests.
- [ ] Выполнить cross-build macOS/arm64 без запуска.
- [ ] Не объявлять runtime acceptance на macOS без физического Apple Silicon
  Mac и корпоративного CLI.

**Проверка:**

```powershell
go build -o stepan.exe ./cmd/stepan
go test ./...
$env:GOOS = "darwin"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
go build -o stepan-darwin-arm64 ./cmd/stepan
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
go vet ./...
git diff --check
```

## 4. Live-приёмка после code complete

Live-приёмка не входит в implementation commit и выполняется на машине с
доступом к корпоративному CLI.

- [ ] Зафиксировать Stepan commit, SDK version, OS/arch, CLI name и разрешённый
  абсолютный path, corporate version и build ID.
- [ ] Выполнить `CLAUDE-M01`: authoritative PATH-name и decoy `claude`.
- [ ] Выполнить `CLAUDE-M02`: пять файловых tools, запрет shell/MCP/network и
  Git write boundary.
- [ ] Выполнить `CLAUDE-M03`: normal close, cancel, process inventory и
  стабильность workspace после контрольной точки.
- [ ] Пройти полный `/idea`, включая вопрос, изменение, `/approve` и второй
  независимый flow.
- [ ] Повторить обязательные сценарии на каждой платформе, для которой
  заявляется поддержка корпоративного build.
- [ ] Сохранить evidence в согласованном внутреннем расположении без secrets и
  не добавлять локальные пути/credentials в публичный репозиторий.

Любой `FAIL` блокирует объявление поддержки. Если `CLAUDE-M03` обнаруживает
оставшийся процесс или продолжающиеся записи, создать новую спецификацию на
launcher/custom transport containment до выпуска Claude runtime.

## 5. Трассировка критериев спецификации

| Критерии спецификации | Основные задачи |
|---|---|
| 1, 7 | CC-01–CC-03, CC-13 |
| 2–4 | CC-04, CC-07, CC-11, CLAUDE-M01 |
| 5–6 | CC-01–CC-03 |
| 8–10 | CC-10–CC-13 |
| 11–12 | CC-06, CC-09, CC-10, CC-13 |
| 13–18 | CC-07, CC-08, CC-11, CC-13, CLAUDE-M02 |
| 19–20 | CC-12, CC-13, CLAUDE-M03 |
| 21–23 | CC-05, CC-13, CC-14 |
| 24 | CC-14, CLAUDE-M01–CLAUDE-M03 |

## 6. Definition of Done

### Code complete

- Все CC-01–CC-14 приняты последовательно.
- `specflow` не зависит от provider-specific packages.
- Default Codex flow не изменился и сохраняет process containment.
- Claude runtime использует exact SDK version, authoritative CLI PATH-name,
  разрешённый absolute path внутри SDK, общий envelope и пять файловых tools.
- Автоматические tests, vet, Windows build и Darwin cross-build проходят без
  настоящего Claude.
- Документация честно показывает live acceptance как `BLOCKED`, если она ещё не
  выполнена.

### Поддержка корпоративного форка подтверждена

- Code complete выполнен.
- Все обязательные строки ручного плана имеют `PASS` на целевой платформе.
- В evidence зафиксирована точная матрица Stepan/SDK/CLI/OS.
- После normal close и cancel не остаётся наблюдаемых процессов или изменений
  workspace, связанных с проверяемой сессией.
- Не потребовалось включить shell, MCP, hooks, plugins или неограниченные
  settings для работоспособности форка.
