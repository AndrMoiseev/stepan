# Stepan: спецификация поддержки Claude Code-совместимого CLI

Статус: черновик

Идентификатор: `claude-cli-support`

## 1. Цель

Добавить второй агентский runtime для текущего интерактивного workflow Stepan.
Пользователь выбирает Claude Code-совместимый CLI, включая корпоративный форк,
и проходит тот же `/idea` flow, что и с Codex: уточнение идеи, создание
спецификации, вопросы, изменения и явное утверждение.

Интеграция использует неофициальный Go SDK
[`github.com/severity1/claude-agent-sdk-go`](https://github.com/severity1/claude-agent-sdk-go).
SDK запускает выбранный CLI и обслуживает его streaming/control protocol;
Stepan сохраняет владение workflow, строгой проверкой результатов и границами
изменений Git working tree.

Результат — две реальные реализации небольшого agent runtime contract без
Codex-типов в `internal/specflow` и без универсального provider framework.

## 2. Пользовательские истории

### US-1. Выбрать Claude runtime

Как разработчик, я хочу явно выбрать Claude при запуске Stepan, чтобы пройти
существующий workflow с другим агентским CLI.

### US-2. Указать корпоративный executable

Как корпоративный пользователь, я хочу передать абсолютный путь к разрешённому
форку Claude CLI, чтобы Stepan запускал именно его независимо от содержимого
`PATH`, имени файла и официального branding.

### US-3. Сохранить поведение workflow

Как пользователь, я хочу получить те же переходы, файлы и проверки `/idea`, что
и при использовании Codex, чтобы выбор агента не менял прикладной контракт.

### US-4. Не позволять агенту запускать внешние инструменты

Как оператор, я хочу, чтобы Claude только читал и изменял разрешённые файлы, а
shell-команды, тесты, Git, сеть и другие внешние инструменты оставались под
контролем Stepan.

### US-5. Понятно завершить или отменить сессию

Как пользователь, я хочу штатно закрыть или отменить Claude-сессию через
Stepan, не открывая Claude CLI отдельно.

## 3. Термины

- **Agent runtime** — provider-neutral интерфейс создания логической сессии,
  выполнения одного хода, прерывания и закрытия.
- **Claude Code-совместимый CLI** — executable, который поддерживает набор
  аргументов и protocol, ожидаемые закреплённой версией
  `claude-agent-sdk-go`. Это может быть официальный Claude Code или
  корпоративный форк.
- **Agent executable** — фактический бинарник выбранного runtime.
- **Ход** — один запрос контроллера к агенту с prompt, ожидаемой JSON Schema и
  политикой записи.
- **Внешний инструмент** — команда или сервис за пределами встроенных файловых
  инструментов Claude. Его lifecycle принадлежит Stepan, а не Claude runtime.

## 4. Границы

### Входит

- выбор `codex` или `claude` при запуске `cmd/stepan`;
- сохранение `codex` как значения по умолчанию;
- обязательный абсолютный путь к executable для Claude;
- возможность передать абсолютный путь и для Codex;
- поддержка пути с пробелами и basename, отличным от `claude`;
- прямой запуск без shell-интерполяции;
- provider-neutral runtime contract, достаточный текущему `specflow`;
- адаптация существующего Codex runtime к этому контракту без изменения его
  поведения;
- новый `internal/agentruntime/claudeapp` поверх `claude-agent-sdk-go` Client API;
- одна долгоживущая SDK connection на интерактивный процесс Stepan;
- отдельная логическая Claude session на каждый `/idea`;
- строгий structured output и локальная проверка схемы каждого хода;
- встроенные инструменты `Read`, `Write`, `Edit`, `Glob`, `Grep`;
- read-only и single-write-root политики текущего workflow;
- штатные close, interrupt, crash recovery и диагностические ошибки;
- автоматические тесты без установленного Claude;
- отложенная live-приёмка на машине с корпоративным CLI.

### Не входит

- `Bash`, PowerShell или любой другой shell tool Claude;
- MCP, hooks, plugins, skills, subagents, agent teams и background tasks;
- `WebFetch`, `WebSearch` и другие сетевые инструменты Claude;
- реализация внешних инструментов или нового tool orchestration protocol;
- выполнение тестов, Git-команд или сборки процессом Claude;
- process-tree containment для Claude, launcher shim, custom SDK transport или
  fork SDK;
- sandbox как замена allowlist и Git post-check;
- resume Claude session после перезапуска Stepan;
- одновременные ходы или параллельные Claude sessions;
- выбор модели, effort, budget или пользовательская настройка system prompt;
- установка, обновление и авторизация Claude CLI;
- гарантия совместимости любого форка без прохождения ручного плана;
- Linux, Intel Mac, signing и notarization.

В этой доработке внешний tool request не добавляется, потому что текущий
`/idea` flow не требует запуска команд. Будущий workflow должен завершать ход
структурированным запросом, после чего Stepan валидирует и выполняет инструмент
снаружи Claude, а результат передаёт в следующий ход.

## 5. Пользовательский интерфейс запуска

`cmd/stepan` получает два аргумента:

```text
--agent codex|claude
--agent-cli <absolute-path>
```

Правила:

1. Без аргументов сохраняется текущее поведение: `--agent codex`, executable
   `codex` разрешается существующим способом через `PATH`.
2. Для `--agent claude` аргумент `--agent-cli` обязателен.
3. Если `--agent-cli` передан для любого provider, его значение должно быть
   абсолютным путём к существующему regular file. Относительный путь и каталог
   отклоняются до запуска runtime.
4. Явный путь авторитетен: Stepan не вызывает `exec.LookPath`, не подставляет
   `claude` или `codex` и не выбирает другой executable после ошибки.
5. Basename, расширение файла и текст `--version` не используются как
   идентификатор provider. Совместимость Claude подтверждается успешным SDK
   handshake и conformance-сценариями.
6. Путь передаётся SDK через `WithCLIPath` без shell и строковой сборки команды.
7. Неизвестный provider, пропущенный путь Claude и невалидный путь завершают
   запуск с кодом `2` и понятной ошибкой.
8. В диагностике можно показывать нормализованный путь к executable, но нельзя
   печатать environment, токены, prompts или содержимое файлов.

Переменная окружения или project config для выбора provider в эту версию не
вводятся: один явный CLI-механизм предотвращает неоднозначный приоритет
настроек.

## 6. Agent runtime contract

Provider-neutral типы размещаются в отдельном внутреннем пакете. Контракт
содержит только фактически используемые операции:

- создать новую логическую session/thread в текущем workspace;
- выполнить строго один последовательный ход с prompt, JSON Schema и политикой;
- вернуть один JSON object либо ошибку;
- прервать активный ход;
- закрыть runtime.

Thread handle является непрозрачным для `specflow`: контроллер хранит и
возвращает его runtime, но не читает provider-specific ID и не создаёт handle
самостоятельно. `specflow` не импортирует `codexapp` или `claudeapp`.

Политика хода описывает только текущие возможности:

- workspace является единственным readable root;
- read-only ход не имеет writable root;
- write-ход имеет ровно один канонический writable root внутри workspace;
- network и внешние команды запрещены для обоих provider.

Функции создания read-only и single-write-root policy переходят из
Codex-specific API в provider-neutral слой. Невалидный или выходящий из
workspace writable root отклоняется до обращения к агенту.

Ошибки закрытого runtime, активного хода, прерывания и аварийного завершения
становятся provider-neutral. Provider может добавлять контекст через wrapping,
но `specflow` не ветвится по конкретному SDK error type.

## 7. Поведение Claude runtime

### 7.1. Lifecycle

- Runtime создаётся лениво при первом `/idea`, как текущий Codex runtime.
- `claudeapp` создаёт один SDK Client и вызывает `Connect` один раз.
- Каждый `StartThread` создаёт новый непустой локально уникальный session ID.
- Ходы выполняются через `QueryWithSession` с ID соответствующего handle.
- Только один ход может быть активен в runtime; конкурентный вызов
  завершается provider-neutral `ErrTurnInProgress`.
- `/approve` освобождает handle на стороне `specflow`, но не закрывает SDK
  Client. Следующий `/idea` получает новый session ID той же connection.
- Protocol/connection error делает runtime нездоровым. `Session` закрывает и
  удаляет его; следующий `/idea` создаёт новый runtime.
- Невалидный прикладной envelope завершает текущий flow fail-closed, но сам по
  себе не считается доказательством поломки SDK connection.
- Durable resume и чтение глобального списка Claude sessions не используются.

### 7.2. Structured output

SDK Client принимает output format при создании, тогда как текущий `specflow`
задаёт отдельную схему на каждый ход. Поэтому Claude connection получает один
закрытый **flow envelope schema**, являющийся объединением всех допустимых
ответов текущего `/idea` flow.

Envelope допускает только существующие варианты:

- `NEEDS_INPUT` с одним `message`;
- `READY_TO_WRITE` с `spec_id`;
- `WRITTEN`;
- `ANSWERED` с `message`;
- `READY_TO_UPDATE`;
- `UPDATED`.

Для каждого хода контроллер по-прежнему передаёт более узкую stage schema и
декодирует результат существующей строгой функцией. Следовательно, валидный для
общего envelope, но недопустимый на текущей стадии status приводит к ошибке и
не меняет состояние workflow.

Claude adapter принимает результат только если:

- SDK вернул terminal result без признака ошибки;
- structured output присутствует и не равен `null`;
- значение является ровно одним JSON object без trailing data;
- нет конфликтующих terminal results;
- object проходит stage schema/decoder контроллера.

Свободный текст, thinking и промежуточные сообщения могут использоваться для
прогресса или диагностики, но не меняют состояние. При отсутствии корректного
structured output adapter работает fail-closed и не извлекает JSON из Markdown
code fence или произвольного текста.

### 7.3. Инструменты и permissions

SDK настраивается точным набором доступных инструментов через `WithTools`:

```text
Read
Write
Edit
Glob
Grep
```

Preset полного набора Claude Code tools и `bypassPermissions` запрещены.
Permission mode остаётся default. Thread-safe `CanUseTool` callback применяет
активную policy:

- `Read`, `Glob` и `Grep` разрешены только внутри канонического workspace;
- `Write` и `Edit` запрещены в read-only ходе;
- в write-ходе `Write` и `Edit` разрешены только внутри единственного
  writable root;
- отсутствующий, неоднозначный или неканонизируемый path отклоняется;
- неизвестный tool отклоняется;
- allow result не создаёт постоянного или session-wide permission grant.

Отсутствие callback для автоматически разрешённых CLI операций не считается
границей безопасности. Жёсткая граница состоит из точного `WithTools`,
ограниченного workspace, отсутствия дополнительных directories и независимого
Git snapshot/post-check после write-turn. Любое новое изменение вне разрешённого
каталога завершает flow ошибкой и не удаляется автоматически.

### 7.4. Изоляция конфигурации

Stepan не передаёт SDK MCP servers, hooks, plugins, agents или дополнительные
directories. Skills явно отключаются. User/project/local setting sources не
подключаются через SDK; настройки, необходимые корпоративному executable для
собственной авторизации, остаются ответственностью этого executable и не
расширяют tool allowlist Stepan.

В environment Claude subprocess явно устанавливаются поддерживаемые CLI
переключатели отключения background tasks и agent view. Точные имена
переменных закрепляются тестом для выбранной версии SDK/совместимого CLI.

Если корпоративный форк не может пройти авторизацию без загрузки setting source,
который одновременно включает hooks, MCP или дополнительные tools, он не
считается совместимым с первой версией. Такое расхождение требует пересмотра
спецификации, а не скрытого ослабления policy.

## 8. Process lifecycle и принятый риск

Claude adapter использует штатный subprocess transport SDK без fork, launcher
shim и собственного `processjob`:

- штатное закрытие вызывает `Disconnect` ровно один раз;
- отмена best-effort вызывает SDK `Interrupt` с ограниченным ожиданием, затем
  `Disconnect`;
- повторные `Interrupt`/`Close` идемпотентны на уровне Stepan adapter;
- `Ctrl+C` сохраняет пользовательский exit code `130`;
- stderr SDK/CLI ограничивается по размеру и используется только для
  диагностики ошибки.

Stepan не заявляет доказанную очистку произвольного дерева потомков Claude.
Риск принят для первой версии, потому что Claude не получает shell, MCP, hooks,
plugins, background tasks или другие предусмотренные способы запуска внешних
процессов. Прямой CLI process всё равно обязан завершиться.

Сценарий `CLAUDE-M03` проверяет фактическое поведение корпоративного форка. Если
после close/cancel остаётся связанный процесс или продолжается изменение
workspace, поддержка получает статус `FAIL`, а containment становится
обязательным до выпуска.

## 9. Совместимость корпоративного форка

Поддержка описывается матрицей, а не предположением о vendor:

| Компонент | Фиксируется в evidence |
|---|---|
| Stepan | commit/build |
| Go SDK | точная версия из `go.mod` |
| CLI | абсолютный путь, версия и корпоративный build ID |
| Платформа | OS/architecture |
| Проверка | результаты `CLAUDE-M01`–`CLAUDE-M03` |

Версия SDK закрепляется точно, без диапазона и автоматического обновления.
Обновление SDK или корпоративного CLI требует автоматического regression suite
и повторной ручной приёмки. Текст версии CLI может отличаться от официального;
единственным функциональным критерием является protocol/tool conformance.

Поддерживаемые платформы совпадают с Stepan: Windows/amd64 и macOS/arm64.
Live-проверка требуется на каждой платформе, для которой заявляется поддержка
конкретного корпоративного build.

## 10. Ошибки и диагностика

Пользователь должен различать как минимум:

- отсутствующий или невалидный абсолютный путь;
- невозможность запустить executable;
- несовместимый protocol/handshake;
- ошибку авторизации корпоративного CLI;
- отказ permission policy;
- ошибочный terminal result;
- отсутствующий/`null` structured output;
- аварийное завершение CLI;
- отмену оператором.

Ошибка не должна содержать полный environment, токены, содержимое prompts или
файлов. Provider name и выбранный абсолютный executable допустимы. После ошибки
хода UI возвращается в то же безопасное состояние, которое определено текущим
`specflow`: runtime crash сбрасывает runtime, прикладная ошибка сбрасывает flow,
а частично записанные файлы не удаляются автоматически.

## 11. Критерии приёмки

1. Запуск `stepan` без новых аргументов сохраняет текущий Codex flow и тесты.
2. `--agent claude` без `--agent-cli` завершается кодом `2` до запуска runtime.
3. Claude принимает абсолютный путь с пробелами и basename, отличным от
   `claude`; executable `claude` из `PATH` при этом не используется.
4. Несуществующий, относительный путь или каталог отклоняются понятной ошибкой.
5. `specflow` не импортирует `codexapp`, `claudeapp` или SDK types.
6. Codex и Claude реализуют один минимальный runtime contract.
7. Все существующие Codex acceptance tests проходят без изменения наблюдаемого
   поведения.
8. Первый `/idea` лениво создаёт один Claude SDK Client; до него CLI не
   запускается.
9. Каждый `/idea` получает новую логическую Claude session, а ходы одного flow
   сохраняют контекст.
10. Второй утверждённый flow переиспользует connection, но не контекст первого.
11. На каждом ходе принимается только допустимый для этой стадии JSON object.
12. `null`, текст вместо object, trailing data, ошибочный status или SDK
    terminal error завершают flow fail-closed.
13. Claude видит только `Read`, `Write`, `Edit`, `Glob`, `Grep`.
14. Read-only ход не может изменить файл.
15. Write-ход может менять только текущий `docs/specs/<spec-id>/`.
16. Запрос `Bash`, MCP, hook, plugin, web tool или subagent не выполняется.
17. Новое изменение вне writable root обнаруживается Git post-check независимо
    от результата Claude.
18. Внешняя команда не запускается процессом Claude.
19. Crash CLI завершает текущий flow; следующий `/idea` создаёт новый runtime.
20. `Ctrl+C` во время Claude turn вызывает interrupt/close и завершает Stepan с
    кодом `130` без зависания.
21. Unit и fake integration suite не требуют Claude, корпоративной сети или
    авторизации.
22. Точная версия SDK присутствует в `go.mod`/`go.sum`; SDK types не выходят за
    `internal/agentruntime/claudeapp`.
23. `go test ./...`, `go vet ./...` и `git diff --check` проходят.
24. Поддержка корпоративного форка не объявляется принятой, пока обязательные
    сценарии ручного плана не получили `PASS` на целевой платформе.

## 12. Стратегия проверки

### Автоматически

- unit tests provider-neutral policy и CLI configuration;
- Codex regression после извлечения contract;
- fake Claude Client/Transport для session routing, message decoding, ошибок и
  lifecycle;
- table-driven permission tests для всех пяти tools, относительных/абсолютных
  путей, symlink/reparse escape и read-only/write policy;
- schema tests общего envelope и всех stage decoders;
- fake end-to-end `/idea` для обоих provider;
- повторные lifecycle tests и race detector для затронутых пакетов.

### Вручную

Live-проверка выполняется по
[manual-test-plan.md](manual-test-plan.md) на машине с доступом к корпоративному
CLI. Отсутствие такого доступа на машине разработки является допустимой
причиной `BLOCKED` для live-приёмки, но не разрешает отметить поддержку
конкретного форка как подтверждённую.

## 13. Связанные документы

- [Детальный план реализации](implementation-plan.md)
- [План ручной приёмки](manual-test-plan.md)
- [Роадмап](../../implementation-roadmap.md#1-поддержка-второго-агентского-cli)
- [ADR 0002: текущий архитектурный baseline](../../adr/0002-current-stack-and-architecture.md)
