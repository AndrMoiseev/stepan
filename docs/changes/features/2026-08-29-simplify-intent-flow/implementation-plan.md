# Stepan: план упрощения intent flow

Статус: черновик для ревью

Основание: [спецификация упрощённого intent flow](specification.md)

## 1. Цель плана

Перевести текущий `/feature` с набора stage-specific prompts, schemas и
per-turn permissions на один продолжительный intent dialogue с двумя исходами
`message | draft`, внешним artifact root и append-only `mem-log.md`.

План сохраняет provider parity: ни одна задача не считается завершённой, если
семантика подтверждена только для Codex или только для Claude. Изменения общего
interface выполняются раньше adapter-specific реализации и раньше переписывания
`specflow`.

## 2. Правила реализации

- Все команды выполняются из корня репозитория.
- Требуется Go 1.26.5.
- Каждый шаг оставляет сборку и затронутые package tests зелёными.
- Общие инварианты проверяются через provider-neutral test suite, а не
  дублируются только в отдельных adapters.
- `specflow` не импортирует `codexapp` или `claudeapp`.
- Stepan остаётся единственным писателем project `intent.md` и `mem-log.md`.
- Агент никогда не получает Git workspace как writable root.
- Artifact root создаётся за пределами workspace и передаётся adapter как
  точный абсолютный путь, а не как родительский системный temp.
- Исторические change-документы не переписываются; фактический текущий flow
  обновляется в `docs/feature-flow.md` после изменения кода.
- Существующие изменения пользователя в working tree не откатываются и не
  перезаписываются.

## 3. Зависимости и порядок

```text
IF-01 domain contracts and schemas
  ├─ IF-02 thread-scoped runtime interface
  │    ├─ IF-03 Codex adapter
  │    └─ IF-04 Claude adapter
  └─ IF-05 feature storage and mem-log
          └─ IF-06 draft publisher and diff review

IF-01 + IF-02 + IF-03 + IF-04 + IF-05 + IF-06
  └─ IF-07 intent controller and prompts
       └─ IF-08 interactive UI
            └─ IF-09 failure, interrupt and cleanup
                 └─ IF-10 end-to-end verification and docs
```

Adapter tasks `IF-03` and `IF-04` можно выполнять независимо после фиксации
общего interface. Остальные задачи выполняются последовательно, чтобы не
поддерживать одновременно несколько временных state machines.

## 4. Задачи реализации

### IF-01. Доменные контракты intent flow

**Цель:** заменить vocabulary спецификации и stage statuses на минимальные
типы intent dialogue.

**Изменения:**

- В `internal/specflow` ввести отдельные типы для:
  - результата генерации `feature_id`;
  - `kind: message | draft`;
  - decision metadata с `author`, `decision`, `rationale`, `alternatives` и
    `supersedes`;
  - последовательных идентификаторов decision-записей.
- Добавить строгую JSON Schema для одноразового ID request.
- Добавить одну неизменную JSON Schema основного thread.
- Запретить неизвестные поля и неправильные комбинации `kind`/`message`.
- Валидировать непустые decision и rationale, допустимого автора и ссылки
  `supersedes` только на уже существующие решения.
- Удалять старые `WRITTEN`, `ANSWERED`, `NEEDS_INPUT`, `READY_TO_UPDATE` и
  `UPDATED` только после перевода всех callers и тестов; не создавать поверх
  них compatibility layer, который сохранит старую state machine.

**Тесты:**

- happy-path обоих main envelopes;
- пустой и невалидный `feature_id`;
- reserved Windows names;
- неизвестные поля и trailing JSON;
- `message` у `draft` и отсутствие `message` у `message`;
- пустые decisions, несколько решений, все допустимые authors;
- неизвестные и будущие `supersedes`.

**Готово, когда:** контракты полностью выражают AC-1 и AC-7 без ссылок на
старые stage statuses.

### IF-02. Глубокий interface thread-scoped agent session

**Цель:** перенести постоянные инструкции и файловые полномочия с каждого turn
на interface создания thread.

**Изменения:**

- Заменить `StartThread()` конфигурацией thread, содержащей минимум:
  - bootstrap instructions;
  - output schema логического разговора;
  - read-only workspace;
  - optional single writable artifact root.
- Упростить `RunTurn`: caller передаёт thread и новый пользовательский input,
  но не меняет write policy между turns.
- Закрепить инвариант последовательного выполнения turns.
- Спрятать внутри adapter добавление bootstrap instructions в первый реальный
  provider request, если transport не имеет отдельного system-instruction
  поля.
- Разрешить adapter технически использовать более широкий immutable envelope,
  если он локально валидирует узкую schema конкретного thread.
- Добавить provider-neutral conformance suite, который принимает runtime
  factory и проверяет один и тот же interface.
- Обновить fake runtime так, чтобы тесты `specflow` наблюдали только semantic
  session config и user inputs.

**Conformance-сценарии:**

- bootstrap instructions передаются ровно один раз;
- второй и последующие turns содержат только новый input;
- thread сохраняет один artifact root до закрытия;
- попытка caller изменить root между turns невозможна через interface;
- read-only thread не получает writable root;
- два threads одного runtime имеют независимые configs;
- ошибка или close инвалидирует handle по существующим правилам.

**Готово, когда:** deletion test для per-turn policy проходит — удаление
`TurnPolicy` из callers не размазывает permission logic по `specflow`.

### IF-03. Codex adapter с внешним artifact root

**Цель:** реализовать новый thread interface поверх Codex App Server без записи
агента в workspace.

**Изменения:**

- Сохранять нормализованный artifact root в Codex thread handle.
- Для каждого внутреннего `turn/start` передавать одинаковую sandbox/approval
  семантику, даже если protocol требует повторения параметров.
- Разрешать file-change approvals только внутри точного artifact root.
- Отклонять workspace writes, соседние temp paths, symlink/junction escapes,
  protected names и расширение permissions.
- Не разрешать network, command execution, session grants или изменение
  execpolicy.
- Передавать bootstrap instructions в первый turn и не дублировать их в
  последующих inputs.
- Добавить автоматический protocol integration test с fake App Server, который
  запрашивает approved write за пределами cwd, но внутри artifact root.

**Тесты:**

- approval внутри внешнего root принимается;
- запись в workspace и соседний temp отклоняется;
- абсолютные Windows paths, пути с пробелами и macOS temp paths;
- lexical и link-based escape;
- повторные turns используют тот же root;
- bootstrap появляется только в первом App Server request;
- interrupt и runtime crash сохраняют существующее поведение.

**Готово, когда:** Codex проходит общий conformance suite и автоматический
protocol integration test внешнего artifact root.

### IF-04. Claude adapter с внешним artifact root

**Цель:** устранить текущее ограничение Claude adapter, требующее writable root
внутри workspace, и сохранить ту же семантику, что у Codex.

**Изменения:**

- Заменить workspace-only разрешение путей моделью двух явно разрешённых roots:
  read-only workspace и read-write artifact root.
- Для `Read`, `Glob` и `Grep` разрешить workspace и artifact root согласно
  нуждам диалога; для `Write` и `Edit` — только artifact root.
- Проверять canonical containment и link escapes отдельно для каждого root.
- Не превращать системный temp directory в общий additional directory.
- Сохранить запрет `Bash`, network, MCP, hooks, plugins и других внешних
  инструментов.
- Сопоставить bootstrap instructions и output schema с возможностями SDK;
  immutable SDK schema может быть union, но результат каждого thread проходит
  локальную узкую валидацию.
- Сохранить `QueryWithSession` и один session ID на основной intent dialogue.

**Тесты:**

- полный общий conformance suite;
- чтение workspace и временного draft;
- запись и edit внешнего `intent.md`;
- запрет записи workspace и чтения произвольного внешнего пути;
- Windows/macOS canonical path cases;
- корпоративный client fake получает bootstrap один раз и user inputs по
  порядку;
- disconnect, interrupt и unhealthy runtime без regressions.

**Готово, когда:** Claude adapter принимает ту же thread config и демонстрирует
те же наблюдаемые разрешения, что Codex.

### IF-05. Feature naming, artifact root и append-only `mem-log.md`

**Цель:** сосредоточить durable storage и naming внутри Stepan.

**Изменения:**

- Реализовать одноразовый feature-ID request до основной сессии.
- Валидировать semantic ID независимо от provider.
- Формировать dated ID из локальной даты `YYYY-MM-DD`.
- Выбирать минимальный свободный суффикс `-2`, `-3`, ... без перезаписи
  существующего каталога.
- Создавать feature directory и `mem-log.md` до основной сессии.
- Создавать уникальный artifact root через OS temp API за пределами workspace.
- Ввести модуль append-only журнала с небольшим interface вида «добавить
  событие/сообщение/решения», скрывающим Markdown formatting и нумерацию.
- Записывать пользовательский input до вызова агента.
- После валидации ответа записывать видимое сообщение агента и decisions.
- Назначать decision IDs внутри journal module и валидировать `supersedes`.
- Считать ошибку append терминальной для flow.
- Не использовать `mem-log.md` для resume.

**Форматные записи журнала:**

- создание feature и исходный brief;
- видимое сообщение пользователя;
- видимое сообщение агента;
- решение пользователя или агента;
- draft published/reviewed/applied/rejected;
- rework comment;
- approve, cancel и error.

Точный Markdown layout фиксируется golden tests. Он может эволюционировать
только append-compatible способом в рамках этого изменения.

**Тесты:**

- русские и длинные briefs передаются ID generator дословно;
- одинаковый ID два и три раза за одну дату;
- существующий каталог никогда не изменяется при выборе имени;
- дата инъецируется clock dependency, а не читается напрямую в тестах;
- feature directory содержит только журнал до первого draft;
- полный visible transcript в правильном порядке;
- decision IDs, alternatives и supersedes;
- старые bytes журнала остаются неизменными после append;
- сбой append прекращает flow;
- temp root находится вне canonical workspace.

**Готово, когда:** AC-1, AC-2 и AC-10 проходят без agent write access к
`mem-log.md`.

### IF-06. Проверка, публикация и review draft

**Цель:** сделать Stepan единственным писателем project `intent.md`.

**Изменения:**

- Использовать фиксированный `<artifact-root>/intent.md`; не принимать путь от
  агента.
- Перед каждым turn сохранять достаточно evidence, чтобы отличить новый или
  изменённый draft от stale-файла предыдущего turn.
- При `kind: draft` проверять:
  - canonical containment;
  - обычный файл без symlink/junction escape;
  - изменение в текущем turn;
  - возможность безопасно прочитать полное содержимое.
- Первый draft атомарно публиковать как project `intent.md` без отдельного
  подтверждения.
- Последующие drafts сравнивать с опубликованным файлом и строить unified diff.
- До выбора пользователя не изменять project `intent.md`.
- Реализовать review outcomes:
  - apply — атомарная замена project file;
  - reject — project file без изменений;
  - rework — комментарий возвращается в тот же thread, project file без
    изменений.
- Записывать все outcomes через journal module.
- Не использовать move из недоверенного temp: Stepan читает проверенный файл и
  сам создаёт project file.

**Тесты:**

- первый draft публикуется автоматически;
- отсутствующий, directory, symlink и escaped draft отклоняются;
- stale draft не публикуется повторно;
- diff для добавления, удаления и замены строк;
- apply меняет только target `intent.md`;
- reject и rework не меняют project file;
- rework использует тот же thread;
- исходно грязный working tree сохраняется;
- сбой атомарной записи не оставляет частично записанный `intent.md`.

**Готово, когда:** AC-8 и AC-9 проверяются через interface publisher/reviewer,
не через внутренние детали UI.

### IF-07. Новый intent controller и bootstrap prompt

**Цель:** заменить текущую stage machine одним dialogue controller.

**Изменения:**

- Переписать `internal/specflow.Controller` вокруг следующих состояний:
  - ожидание brief;
  - генерация ID;
  - активный диалог до первого draft;
  - активный диалог с опубликованным intent;
  - ожидание решения по diff;
  - ожидание rework comment;
  - завершение.
- Не вводить отдельные состояния вопроса и анализа изменения.
- Создать один bootstrap prompt, содержащий полный контракт основной сессии и
  абсолютный artifact root.
- Передавать последующие пользовательские сообщения без stage-specific
  system-шаблонов.
- Позволить агенту отвечать `message` неограниченное число turns до draft.
- Требовать перед каждым последующим draft заново читать опубликованный
  `intent.md`, чтобы apply/reject outcome определялся project state, а не
  предположением из памяти thread.
- Проверять, что decisions записаны до перехода к следующему вводу.
- После первого draft оставлять тот же thread активным для обычных вопросов и
  revisions.
- Реализовать `/approve` только после опубликованного intent.
- Удалить старые prompt templates и decoders после миграции всех тестов.

**Тесты controller:**

- brief в команде и отдельным вводом;
- ID request и основной thread различны;
- один и несколько `message` до первого draft;
- bootstrap один раз, следующие inputs без повторных правил;
- первый draft, обычный вопрос после draft и новая версия;
- decision metadata во всех точках;
- apply/reject/rework transitions;
- `/approve` до draft запрещён, после draft завершает flow;
- invalid output, journal error, runtime error и interrupted turn.

**Готово, когда:** controller больше не имеет методов `AskQuestion`,
`ProposeChange`, `SubmitChangeAnswer` и не знает per-turn write policy.

### IF-08. Единый интерактивный UI

**Цель:** убрать пользовательское разделение на вопрос и изменение.

**Изменения:**

- После `/feature` показывать обычный dialogue input для каждого следующего
  сообщения.
- Показывать `message` агента и сразу ждать новый пользовательский input.
- После первого draft показывать путь к опубликованному `intent.md` и сохранять
  диалог активным.
- При последующем draft показывать читаемый diff и три действия:
  - применить;
  - отклонить;
  - доработать.
- Для «доработать» открыть текстовый ввод комментария и после него вернуть
  пользователя к тому же review loop.
- Оставить `/approve` отдельным явным завершением.
- Обновить названия экранов с «Specification» на «Intent».
- Сохранить accessible mode, terminal checks и `Ctrl+C` semantics.

**Тесты:**

- UI model не содержит отдельных question/change menus;
- message loop с одиночными и пакетными вопросами;
- diff screen и каждое из трёх действий;
- пустой rework comment обрабатывается согласно общей проверке input;
- `/approve` и возврат в main prompt;
- visible messages передаются journal module дословно;
- terminal rendering не смешивается с controller tests.

**Готово, когда:** пользователь может пройти весь AC-5 и AC-9 без знания
внутренних стадий.

### IF-09. Lifecycle, cleanup и отказоустойчивость

**Цель:** завершать thread и временные ресурсы предсказуемо, не теряя project
history.

**Изменения:**

- На `/approve` сначала append approval event, затем закрывать thread и удалять
  artifact root.
- На штатной ошибке или отмене best-effort записывать событие в `mem-log.md`,
  закрывать thread и удалять artifact root.
- На crash не использовать оставшийся temp для resume.
- Не удалять feature directory, `mem-log.md` или опубликованный `intent.md`.
- Сохранить runtime discard/restart после provider crash.
- Сохранить platform process containment Codex и принятые ограничения Claude.
- Исключить cleanup другого системного temp или temp другого flow через
  canonical identity checks.

**Тесты:**

- approve order: journal → close → cleanup;
- journal failure не маскируется успешным approve;
- interrupt до и после первого draft;
- runtime crash и следующий `/feature` с новым runtime/thread;
- cleanup удаляет только созданный artifact root;
- Ctrl+C code 130 и отсутствие контролируемых Codex потомков;
- Claude disconnect/interrupt regression suite.

**Готово, когда:** AC-11 и AC-13 покрыты автоматическими platform-specific и
provider-neutral тестами.

### IF-10. Сквозная проверка и документация

**Цель:** подтвердить новый flow как фактическое поведение продукта.

**Изменения:**

- Переписать `docs/feature-flow.md` на intent vocabulary и новую state diagram.
- Обновить `docs/changes/features/README.md` с ролью `intent.md` и
  `mem-log.md`, не отменяя `specification.md` для change-документов вроде этой
  спецификации.
- Обновить prompt README и удалить упоминания старых stage prompts.
- Обновить help и пользовательские документы, которые ожидают
  `specification.md` или экраны `/question`/`/change`.
- Проверить ADR 0002 и ADR 0004. Если новый runtime interface и два filesystem
  roots противоречат принятым решениям, обновить существующие ADR или добавить
  узкий новый ADR до code complete.
- Добавить fake end-to-end сценарий полного intent flow для обоих provider
  factories.

**Готово, когда:** документация описывает реализованный flow, все AC имеют
автоматическое evidence.

## 5. Автоматическая проверка

Минимальный набор после каждой затронутой области:

```powershell
go test ./internal/specflow
go test ./internal/agentruntime/...
go test ./cmd/stepan
```

Полная проверка перед code complete:

```powershell
go build -o stepan.exe ./cmd/stepan
go test ./...
go vet ./...
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

Cross-build macOS/arm64:

```powershell
$env:GOOS = "darwin"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
go build -o stepan-darwin-arm64 ./cmd/stepan
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Cross-build подтверждает компиляцию целевой macOS/arm64 сборки.

## 6. Сквозные автотесты

| ID | Сценарий | Обязательное evidence |
| --- | --- | --- |
| `INT-F01` | Длинный brief → semantic ID | валидный dated ID и исходный brief в журнале |
| `INT-F02` | Коллизия ID | существующий каталог не изменён, выбран `-2` |
| `INT-F03` | Несколько уточнений | один main thread, несколько `message`, draft отсутствует |
| `INT-F04` | Первый draft | temp `intent.md` опубликован без отдельного confirm |
| `INT-F05` | Обычный вопрос после draft | Q&A в `mem-log.md`, project intent без изменений |
| `INT-F06` | Apply revision | показан diff, project intent атомарно заменён |
| `INT-F07` | Reject revision | diff записан как отклонённый, project intent прежний |
| `INT-F08` | Rework revision | comment передан в тот же thread, новый diff показан повторно |
| `INT-F09` | Неявное решение агента | author, rationale и alternatives добавлены append-only |
| `INT-F10` | Пересмотр решения | новая decision с `supersedes`, старая запись неизменна |
| `INT-F11` | Workspace write attempt | оба adapters отклоняют запись |
| `INT-F12` | Temp escape attempt | sibling/link escape отклонён |
| `INT-F13` | Stale draft | прежний temp file не публикуется повторно |
| `INT-F14` | Approve | approval logged, thread закрыт, temp удалён |
| `INT-F15` | Crash/cancel | project artifacts сохранены, автоматического resume нет |

## 7. Трассировка критериев

| Критерий | Основные задачи | Проверка |
| --- | --- | --- |
| AC-1 | IF-01, IF-05 | INT-F01, INT-F02 |
| AC-2 | IF-05 | INT-F01, INT-F03 |
| AC-3 | IF-02, IF-07 | INT-F03, INT-F08 |
| AC-4 | IF-07 | INT-F03, controller tests |
| AC-5 | IF-07, IF-08 | INT-F05–INT-F08 |
| AC-6 | IF-02–IF-04 | INT-F11, INT-F12, adapter integration tests |
| AC-7 | IF-01 | schema unit tests |
| AC-8 | IF-06, IF-07 | INT-F04 |
| AC-9 | IF-06, IF-08 | INT-F06–INT-F08 |
| AC-10 | IF-01, IF-05 | INT-F05, INT-F09, INT-F10 |
| AC-11 | IF-08, IF-09 | INT-F14 |
| AC-12 | IF-03, IF-04, IF-10 | conformance suite, adapter integration tests |
| AC-13 | IF-09 | INT-F15 |

## 8. Definition of Done

Изменение завершено, когда одновременно выполнены условия:

- старые question/change/write statuses и prompts больше не участвуют в
  `/feature`;
- один основной thread обслуживает весь intent dialogue;
- `message | draft` и decision metadata строго валидируются;
- Codex и Claude используют thread-scoped внешний artifact root с одинаковой
  семантикой;
- агент не может писать Git workspace;
- первый draft публикуется автоматически, последующие проходят diff review;
- `mem-log.md` append-only и содержит полный visible dialogue, решения и
  review events;
- `/approve` завершает flow и очищает temp без commit;
- `go build`, `go test ./...`, `go vet ./...` и actionlint проходят;
- macOS/arm64 cross-build проходит;
- `docs/feature-flow.md`, help, prompt docs и необходимые ADR согласованы с
  фактическим кодом;
- все критерии приёмки покрыты автоматическими unit, integration, conformance
  или end-to-end тестами.
