# Спецификация поддержки Qwen Code

Связанный утверждённый замысел: [intent.md](intent.md).

## Статус и область документа

Документ определяет наблюдаемое поведение и архитектурные границы поддержки
Qwen Code в существующем `/feature` flow Stepan. Он не разбивает реализацию на
задачи.

Во время подготовки спецификации пользователь уточнил и частично изменил
ограничение из intent Decision D-004. Для Qwen выбран минимальный ACP-профиль:
область чтения задаётся ACP roots и инструкциями, но не является sandbox/ACL;
запись ограничивается ACP approvals и проверяется существующими Git-gates.
Это осознанно слабее заявленной в intent общей технической границы чтения и не
должно скрываться под формулировкой «read-only isolation».

## Текущее поведение

- `stepan` поддерживает `--agent codex|claude`; без флага используется Codex из
  `PATH`.
- Claude требует явный абсолютный `--agent-cli` и работает через
  `claude-agent-sdk-go`; Qwen как provider отсутствует.
- `internal/agentruntime` предоставляет provider-neutral thread contract с
  immutable bootstrap, JSON Schema, Git workspace и одним внешним artifact
  root.
- Codex получает native output schema, read-only sandbox policy, отключённую
  сеть и turn-scoped approvals; Claude получает JSON Schema и ограниченный
  набор файловых инструментов с path callback.
- Agent пишет кандидат документа только во внешний временный artifact root, а
  Stepan валидирует и публикует его в feature directory.
- Git должен быть чистым при создании feature. Перед approval Stepan блокирует
  изменения вне каталога текущей feature; автоматического отката нет.
- Codex process находится под Windows Job Object или macOS process group.
  Claude использует штатный lifecycle SDK без process-tree containment.

## Целевое поведение

Пользователь явно выбирает Qwen через `--agent qwen`. Для каждого логического
thread Stepan запускает отдельный выбранный CLI напрямую как `qwen --acp` с
подключённым при старте artifact root, создаёт одну ACP-сессию и проводит те же
intent, spec, plan, review и resume-сценарии, что с Codex и Claude.

Qwen использует нативные файловые инструменты в изолированной конфигурации без
shell, web и ambient extensions. Git root задаётся как process working directory
и ACP `cwd`, а внешний artifact root подключается ко всему process через
`--include-directories` до ACP handshake. Официальный Qwen не обязан
поддерживать session-scoped `additionalDirectories`; поэтому один process не
разделяется между threads с разными artifact roots. Чтение вне roots
запрещается инструкциями, но не заявляется как технически предотвращённое.
Запросы `write`/`edit` разрешаются только для канонического пути внутри artifact
root текущего process/thread.

Поскольку ACP несовместим с native Qwen `--json-schema`, общий system prompt и
session bootstrap требуют точного JSON-ответа. Stepan строго разбирает и
валидирует финальный текст. При ошибке формата он выполняет не более двух
repair-запросов после первоначального ответа в той же сессии.

## Требования

### REQ-001 — Явный provider Qwen

CLI должен принимать закрытое значение `qwen` в `--agent
codex|claude|qwen`. Qwen используется только после явного выбора; отсутствие
`--agent` сохраняет Codex.

### REQ-002 — Выбор executable

При `--agent qwen` без `--agent-cli` Stepan должен разрешить executable `qwen`
через `PATH`. Переданный `--agent-cli` должен быть существующим regular file по
абсолютному пути и является авторитетным: Stepan не заменяет его executable из
`PATH` и не требует определённого basename или branding.

### REQ-003 — Отсутствие version/vendor gate

Stepan не должен вызывать `--version` как gate, сравнивать номер версии,
поставщика или branding. Совместимость определяется успешным ACP handshake,
обязательными capabilities и фактическим protocol/tool conformance.

### REQ-004 — Прямой ACP transport

Qwen runtime должен запускать выбранный executable напрямую, без shell, в ACP
режиме поверх UTF-8 JSON-RPC/NDJSON stdio. Для каждого thread process получает
канонический artifact root через отдельный аргумент
`--include-directories <artifact-root>` до `--acp` handshake. `qwen serve`, HTTP
daemon, TypeScript/Python/Java SDK и Claude-compatible transport не входят в
production путь.

### REQ-005 — Полный `/feature` flow

Qwen должен поддерживать все существующие пользовательские стадии, команды и
переходы `/feature`: intent dialogue и approval, spec dialogue/review/approval,
plan dialogue/review/approval, material decisions, automatic rework, resume и
завершение flow. Provider не должен менять доменную семантику или доступность
команд.

### REQ-006 — Отдельный process и сессия на thread

Один экземпляр Qwen runtime Stepan должен владеть набором независимо
контролируемых thread handles. Для каждого логического author/reviewer thread
лениво создаются отдельный contained process `qwen --acp` и ровно одна ACP
сессия. Process не переиспользуется другим thread. Ходы одной сессии
последовательны; конкурентный ход того же thread возвращает существующую
provider-neutral ошибку `ErrTurnInProgress`. Закрытие thread закрывает его
session и process tree, не затрагивая другие threads runtime. Число Qwen
processes равно числу открытых Qwen threads; provider-neutral последовательная
семантика вызовов `RunTurn` не меняется.

### REQ-007 — Process и session roots

До запуска thread process Stepan должен канонизировать Git root и назначенный
thread artifact root и подтвердить, что artifact root находится вне Git root.
Child process запускается с Git root как operating-system working directory и с
ровно одним process-wide дополнительным root через
`--include-directories <artifact-root>`. При создании ACP-сессии Stepan должен
передавать тот же Git root как `cwd`.

Stepan не должен зависеть от ACP `sessionCapabilities.additionalDirectories` и
не должен передавать поле `additionalDirectories`, если capability не объявлена
agent. Отсутствие этой session capability у официального Qwen само по себе не
является incompatibility. Несовместимостью являются отсутствие поддержанного
Qwen startup contract `--include-directories` или фактическая невозможность
читать и записывать назначенный artifact root. Stepan не подключает sibling
artifact roots к этому process и не переносит artifact внутрь проекта как
fallback.

### REQ-008 — Bootstrap и schema-инструкция

Process-level system prompt должен содержать общий неизменный контракт: финал
каждого хода — ровно один JSON object по переданной Stepan schema, без Markdown
fence, префикса, суффикса или свободного текста. Role instructions, текущая
машинная schema и session context должны неизменно добавляться в первый prompt
конкретной ACP-сессии. Schema остаётся immutable для lifetime thread.

### REQ-009 — Декодирование ACP-ответа

Stepan должен игнорировать progress/tool notifications как terminal payload и
собирать только финальный assistant response завершённого `session/prompt`.
Ответ принимается, только если он содержит один UTF-8 JSON object без trailing
data и проходит `agentruntime.ValidateOutput` относительно schema thread.
Свободный текст, Markdown, несколько финальных ответов, неизвестный content
type, malformed JSON и schema violation не меняют состояние flow.

### REQ-010 — Ограниченный format repair

После невалидного финального ответа Stepan должен отправить в ту же ACP-сессию
repair prompt с краткой безопасной диагностикой и точной schema. На один
пользовательский ход допускаются три финальных ответа всего: первоначальный и
не более двух repair-ответов. После третьего невалидного ответа следующая
попытка не запускается, возвращается классифицированная protocol error, а
управление передаётся пользователю.

Repair budget применяется только к формату/schema. Permission violation,
process exit, transport corruption, cancellation и session correlation error
не вызывают format repair.

### REQ-011 — Изолированная конфигурация Qwen

Официальный Qwen должен запускаться в safe mode или эквивалентной изолированной
конфигурации. Model-visible tool surface ограничивается нативными аналогами
`Read`, `Write`, `Edit`, `Glob` и `Grep`: `read_file`, `write_file`, `edit`,
`glob`, `grep_search`. Shell, web, ambient/user/project MCP, hooks, extensions,
skills, memory, custom/built-in subagents, background tasks и дополнительные
инструменты должны быть отключены. Несогласованный или расширенный tool surface
завершает preflight fail-closed.

### REQ-012 — Политика чтения минимального профиля

Bootstrap должен явно разрешать чтение только внутри канонических Git root и
artifact root текущего thread. Stepan задаёт Git root как process working
directory и ACP `cwd`, подключает artifact root через `--include-directories` и
отклоняет выходящие за них делегированные `fs/read_text_file` requests, если CLI
использует client-side filesystem mediation.

Для нативных Qwen read/glob/grep, выполняемых внутри CLI без ACP permission
request, Stepan не заявляет технического предотвращения доступа вне roots.
Соблюдение области основывается на prompt и conformance evidence. Эта версия не
должна называться sandboxed read isolation.

### REQ-013 — Политика записи

Native `write_file` и `edit` остаются доступны, но Stepan должен отвечать на ACP
permission request разрешением только тогда, когда:

- существует активный turn и запрос коррелирует с его session/turn/tool ID;
- целевой путь однозначно канонизирован;
- путь находится внутри текущего artifact root после проверки symlink/junction
  escape;
- операция не просит постоянного, session-wide или process-wide расширения
  полномочий.

Запись в Git workspace, другой artifact root, внешний путь или запрос с
неоднозначными path fields отклоняются fail-closed. Решение одноразовое и
turn-scoped.

### REQ-014 — Git gates

Qwen использует существующую общую Git-политику без provider-specific snapshot
вокруг каждого turn: clean index/working tree при создании feature и Git status
перед approval. Изменения вне каталога текущей feature блокируют approval.
Stepan не выполняет автоматический rollback. Git gate является вторым барьером,
а не заменой ACP write policy, и не заявляет контроль ignored-файлов или путей
вне репозитория.

### REQ-015 — Process-tree containment

До начала ACP handshake process каждого Qwen thread должен быть отдельно
подготовлен для назначения в собственный Windows Job Object или macOS process
group существующего `processjob` boundary. Если supervisor для нового thread
создать или назначить нельзя, `StartThread` завершается ошибкой и handle не
публикуется; уже открытые threads не затрагиваются. Все процессы конкретного
дерева должны завершаться при `CloseThread`, а все деревья — при закрытии или
interrupt runtime.

### REQ-016 — Отмена и закрытие

При `Interrupt` активного хода Stepan должен сначала отправить ACP
`session/cancel` в session активного thread, закрыть все pending permission
requests fail-closed и ждать не более трёх секунд. Затем runtime принудительно
закрывает все принадлежащие ему process trees и инвалидирует все thread handles,
сохраняя существующую глобальную семантику `Interrupt`. Если активного хода нет,
`session/cancel` не отправляется, но все handles и process trees всё равно
закрываются.

`CloseThread` инвалидирует только переданный handle и закрывает только его
process tree; остальные threads продолжают работать. `Close` закрывает все
thread processes и идемпотентен. После контрольной точки закрытия thread или
runtime соответствующий Qwen process не должен продолжать отправлять события
или изменять файлы.

### REQ-017 — ACP correlation и fail-closed transport

Stepan должен коррелировать request/response/notification по protocol request
ID и session ID, допуская не более одного активного prompt на session. Duplicate
terminal response, ответ неизвестному request, событие чужой session, terminal
response при pending permission или событие после закрытия делают связанный
thread unhealthy, закрывают его process tree и возвращают provider-neutral
protocol/runtime error. Другие thread processes остаются доступными. Если
нарушение невозможно однозначно связать с одним thread, runtime закрывает все
process trees fail-closed.

### REQ-018 — Preflight совместимости

При первом использовании Qwen Stepan должен до model turn проверить executable,
поддерживаемую платформу, Qwen-compatible startup contract
`--include-directories`, ACP initialize/session lifecycle, `cwd`, prompt/cancel,
permission mediation и требуемую изолированную tool configuration. Проверка не
должна требовать ACP capability `additionalDirectories`: внешний artifact root
подключён к process до handshake. Ошибка должна называть отсутствующую
capability или нарушенный contract без автоматического fallback на Codex,
Claude, другой executable или более широкие полномочия.

Поведение, которое нельзя доказать handshake-ом, подтверждается одинаковым
conformance suite и manual canary-сценариями для официального и стороннего CLI.

### REQ-019 — Ошибки и пользовательская диагностика

Qwen adapter должен отображать provider-neutral причины с безопасным Qwen
context:

- executable отсутствует или не является regular file;
- ACP handshake/capability несовместим;
- выбранный CLI завершился или нарушил protocol;
- permission отклонён;
- structured response исчерпал repair budget;
- ход отменён оператором.

Диагностика может содержать provider `qwen`, выбранный executable и имена
capabilities, но не должна выводить credentials, environment secrets, полные
prompt/response bodies или содержимое запрещённого файла.

### REQ-020 — Provider-neutral state и resume

Qwen session IDs, process IDs, prompts и provider-specific capability payloads
не сохраняются в `state.json` и не входят в document/review fingerprints. После
рестарта `/resume` создаёт новый Qwen process/session и восстанавливает durable
контекст из утверждённых документов, review reports, state и append-only
`mem-log.md`, как для существующих adapters.

### REQ-021 — Runtime metadata

Runtime metadata должен использовать provider `qwen`. Stepan не выбирает и не
переключает модель в этой версии. Если ACP сообщает активную модель, её имя
можно записать в предусмотренные review/runtime metadata; при отсутствии
данных используется существующий placeholder `default`. Модель не влияет на
state equality или fingerprints.

### REQ-022 — Поддерживаемые платформы

Qwen должен поддерживаться в нативных сборках Windows/amd64 и Apple Silicon
macOS/arm64. Поведение CLI selection, ACP framing, path canonicalization,
permission policy, cancellation и containment должно быть эквивалентным на
обеих платформах. Cross-build macOS подтверждает только компиляцию; runtime
приёмка выполняется на физическом Apple Silicon Mac.

### REQ-023 — Регрессия Codex и Claude

Добавление Qwen не должно менять default Codex selection, флаги Claude,
provider-neutral runtime interface, domain flow, document contracts, Git
semantics или существующие process/security решения Codex и Claude.

## Архитектурные и сквозные решения

### DEC-001 — Qwen как третий закрытый provider

Composition root расширяет закрытый выбор `codex|claude` значением `qwen`.
Global provider registry, plugin framework и автоматическое обнаружение
provider не вводятся.

### DEC-002 — ACP stdio вместо SDK и daemon

Интеграционной поверхностью является прямой `qwen --acp` child process.
Решение сохраняет Go-only host, авторитетный пользовательский executable и
стандартный session/permission/cancel protocol без Node/Python sidecar или
долгоживущего HTTP service.

### DEC-003 — Prompt-defined structured response

Из-за несовместимости Qwen ACP с `--json-schema` model-level формат задаётся
system/session instructions, а обязательная гарантия обеспечивается строгим
парсером и schema validation в Stepan. Невалидный текст никогда не становится
доменным envelope.

### DEC-004 — Общий retry limit

Qwen format repair использует существующую семантику `DefaultRetryLimit = 3`:
три ответа всего на цикл. Отдельный безлимитный или provider-specific retry
budget не вводится.

### DEC-005 — Минимальный read profile

Process working directory, ACP `cwd` и startup include root считаются логической
областью, а не ACL. Prompt и conformance ограничивают нативное чтение Qwen;
обязательный container/OS sandbox и Stepan-controlled filesystem tools исключены
из текущей версии.

### DEC-006 — Native tools с ACP write mediation

Qwen сохраняет собственные file tools. Stepan не переimplementирует их, но
остаётся единственным субъектом, разрешающим write/edit requests в текущий
artifact root. Git используется как общий дополнительный gate.

### DEC-007 — Один process на logical thread

Поскольку официальный Qwen подключает дополнительные каталоги на startup
boundary, а не через обязательную session capability, каждый logical thread
получает отдельный contained ACP process и одну session. Artifact root виден
всему своему process через `--include-directories`, что является принятой
границей риска; process не получает artifact roots других threads. Эта
топология сохраняет session-specific writable root без зависимости от
`additionalDirectories` и без расширения artifact location внутрь Git root.
Process ownership и расход ресурсов линейны числу открытых threads; process
освобождается вместе со своим thread.

### DEC-008 — Обязательный process supervisor

Для Qwen выбран lifecycle Codex, а не принятый риск Claude v1: запуск без
process-tree containment запрещён.

### DEC-009 — Protocol conformance вместо версии

Официальный и сторонний CLI оцениваются по одному обязательному ACP/tool
contract. Version allowlist, basename check и branding probe не используются.

### DEC-010 — Паритет Git-политики

Qwen не получает отдельную автоматическую rollback или per-turn snapshot
семантику. Общие Git preflight и approval blockers остаются одинаковыми для
provider.

### DEC-011 — Модель выбирает CLI

Выбор модели, новый CLI-флаг модели и динамический ACP model switch исключены.
Текущая версия использует effective model настроенного пользователем CLI.

## Интерфейсы

### CLI

```text
stepan [--agent codex|claude|qwen] [--agent-cli <absolute-path>]
```

Наблюдаемая семантика:

| Вызов | Результат |
|---|---|
| `stepan` | существующий Codex из `PATH` |
| `stepan --agent qwen` | `qwen` из `PATH`, direct ACP |
| `stepan --agent qwen --agent-cli <abs>` | exact explicit Qwen-compatible CLI |
| `stepan --agent qwen --agent-cli <relative>` | usage error до runtime |
| неизвестный `--agent` | usage error со списком трёх значений |

### Provider-neutral runtime

`internal/agentruntime.Runtime` сохраняет существующие методы `StartThread`,
`RunTurn`, `CloseThread`, `Interrupt`, `Close`. Qwen-specific ACP messages,
capabilities и session IDs не выходят из adapter package.

### ACP lifecycle

Минимальный обязательный обмен:

1. отдельный contained process start для thread с Git root как working directory
   и `--include-directories <artifact-root>`, затем ACP initialize;
2. `session/new` с Git root в `cwd`; `additionalDirectories` не требуется;
3. последовательные `session/prompt`, progress/tool events и один terminal
   response;
4. `session/request_permission` для write/edit и одноразовый ответ Stepan;
5. `session/cancel` при interrupt;
6. при `CloseThread` — локальная инвалидизация одного session handle и закрытие
   только его process tree;
7. при `Interrupt` или runtime `Close` — инвалидизация всех handles и закрытие
   всех принадлежащих runtime process trees.

## Данные и миграции

- Закрытый CLI enum расширяется значением `qwen`; формат durable flow state не
  меняется.
- Qwen process/session/request IDs остаются эфемерными и не требуют migration.
- Существующие feature directories, `state.json`, `mem-log.md`, документы и
  review reports читаются без преобразования.
- Resume может выполняться с Qwen независимо от provider предыдущего процесса,
  поскольку provider thread identity не является durable domain state.
- Новые credentials, Qwen config или model data не записываются Stepan в
  repository. Установка, authentication и исходная конфигурация выбранного CLI
  остаются обязанностью пользователя.

## Ошибки и восстановление

- Configuration/preflight error не создаёт ACP model session и не меняет flow.
- Ошибка после запуска закрывает pending approvals и contained process tree
  затронутого thread; несопоставимая с thread transport error закрывает весь
  runtime.
- Невалидный JSON/schema использует только ограниченный repair loop REQ-010.
- После исчерпания repair budget пользователь может отправить новый явный ход;
  это новый budget, а не скрытая четвёртая попытка.
- Permission denial возвращается модели в рамках текущего ACP prompt; Stepan не
  расширяет policy. Если CLI завершает prompt ошибкой, она классифицируется как
  provider runtime error.
- Git blocker оставляет файлы без автоматического rollback и требует действия
  пользователя существующим способом.
- После process/protocol failure продолжение затронутого flow использует новый
  thread process/session и durable context; повреждённая session автоматически
  не переиспользуется. Незатронутые thread handles сохраняются, если ошибка была
  однозначно локализована.

## Безопасность

- Selected executable запускается напрямую массивом аргументов.
- Explicit path авторитетен и проверяется до запуска.
- Safe/equivalent mode исключает ambient executable surfaces и project/user
  customizations, кроме необходимой пользовательской authentication выбранного
  CLI.
- Network, shell и external tools недоступны модели.
- Write/edit approval fail-closed, одноразовый и ограничен artifact root.
- Process tree контролируется Stepan на обеих платформах.
- Git gates остаются defense-in-depth.
- Чтение нативными Qwen tools не имеет OS-enforced root isolation. Prompt roots
  и conformance снижают риск, но не защищают секреты, доступные security
  principal процесса. Оператор не должен запускать этот профиль под identity с
  данными, которые нельзя доверить выбранному Qwen-compatible CLI.

## Совместимость

- Codex default и Claude explicit-path behavior сохраняются.
- Официальный Qwen и сторонний CLI используют один ACP contract и один
  Qwen-compatible startup contract `--include-directories`.
- Qwen использует один process на открытый logical thread; по сравнению с
  shared-process topology расход процессов масштабируется линейно числу threads.
- Совместимость не обещается только по факту успешного `initialize`: требуются
  startup/cwd roots, tool isolation, write permission mediation, output parsing,
  cancellation и process cleanup.
- Linux, Intel Mac и другие архитектуры не поддерживаются.
- Изменение upstream ACP protocol или Qwen tool semantics требует повторения
  conformance и manual acceptance, но не version gate в production.

## Acceptance criteria

### AC-001 — CLI selection

Traces: REQ-001, REQ-002, REQ-003

Без флагов запускается Codex. `--agent qwen` запускает `qwen` из `PATH`; explicit
absolute `--agent-cli` запускает ровно выбранный файл. Relative/missing path и
unknown provider завершаются usage error без запуска другого CLI.

### AC-002 — ACP transport

Traces: REQ-004, REQ-006, REQ-018

Fake Qwen подтверждает direct stdio initialize, отдельные contained processes и
sessions для двух logical threads и отсутствие SDK/HTTP daemon path. Missing
mandatory capability или startup contract завершает preflight и закрывает
соответствующий contained process. Два открытых thread handles соответствуют
двум process trees; после `CloseThread` одного handle остаётся ровно одно дерево.

### AC-003 — Полный provider parity

Traces: REQ-005, REQ-020, REQ-023

Один provider-neutral conformance suite проходит для Codex, Claude и Qwen по
author/reviewer dialogue, artifact, decisions, rework, approval, close и resume
с одинаковой доменной семантикой.

### AC-004 — Thread roots

Traces: REQ-007, REQ-013

Для каждого из двух Qwen threads argv его process содержит ровно один
канонический `--include-directories` со своим внешним artifact root, working
directory и ACP `cwd` равны Git root, а `session/new` успешно работает без
объявленной capability `additionalDirectories`. Process не получает sibling
artifact root. Write/edit в текущий root разрешается; sibling artifact, Git
workspace, symlink/junction escape и внешний path отклоняются.

### AC-005 — Prompt-defined valid envelope

Traces: REQ-008, REQ-009

Точный финальный JSON object, соответствующий thread schema, принимается как
provider-neutral envelope. Progress events не попадают в payload. Markdown,
prefix/suffix, второй final response, malformed JSON и schema violation не
изменяют state.

### AC-006 — Ограниченный repair

Traces: REQ-009, REQ-010

Fixture с двумя невалидными ответами и третьим валидным завершается успехом в
той же session. Fixture с третьим невалидным ответом не запускает четвёртый
prompt, возвращает protocol error и сохраняет прежнее domain state.

### AC-007 — Tool isolation

Traces: REQ-011, REQ-018

Model-visible inventory содержит только `read_file`, `write_file`, `edit`,
`glob`, `grep_search`. Попытки shell, network, MCP, hook, extension, skill,
memory, subagent или background task отсутствуют либо отклоняются до
исполнения. Расширенный inventory делает CLI несовместимым.

### AC-008 — Минимальная read posture

Traces: REQ-012

Bootstrap перечисляет только logical roots текущего thread; делегированный ACP
read вне roots отклоняется. Manual canary фиксирует фактическое поведение
нативного чтения и явно маркирует отсутствие OS-level гарантии, не выдавая его
за sandbox PASS.

### AC-009 — Write approval correlation

Traces: REQ-013, REQ-017

Write approval принимается только для active session/turn/tool и artifact root.
Duplicate, stale, foreign-session, persistent-grant и ambiguous-path requests
отклоняются fail-closed и не расширяют последующие ходы.

### AC-010 — Git parity

Traces: REQ-014, REQ-023

Qwen требует тот же clean create preflight и получает те же approval blockers,
что Codex/Claude. Qwen-specific per-turn snapshot и automatic rollback
отсутствуют.

### AC-011 — Contained lifecycle Windows

Traces: REQ-015, REQ-016, REQ-022

На Windows/amd64 processes двух Qwen threads назначены в разные Job Objects до
model turn. `CloseThread` и локализованный protocol failure удаляют только
соответствующий process и потомков, сохраняя второй thread. `Interrupt` и runtime
`Close` удаляют оба дерева; невозможность назначения нового Job Object блокирует
только создание нового thread.

### AC-012 — Contained lifecycle macOS

Traces: REQ-015, REQ-016, REQ-022

На физическом Apple Silicon Mac каждый Qwen thread запускается в отдельной
process group. `CloseThread` удаляет только его group, а `Interrupt` и runtime
`Close` удаляют все groups. После контрольной точки соответствующие processes
отсутствуют и файлы не изменяются. Cross-build отдельно подтверждает компиляцию.

### AC-013 — Cancel grace и late events

Traces: REQ-016, REQ-017

Runtime `Interrupt` отправляет `session/cancel` активному thread, закрывает все
approvals и не ждёт более трёх секунд до forced close всех принадлежащих runtime
process trees. Все handles становятся недействительными; late event не меняет
state и не принимается новым runtime.

### AC-014 — Совместимый сторонний CLI

Traces: REQ-002, REQ-003, REQ-018, REQ-019

Executable с нестандартным basename и без ожидаемой branding/version строки
проходит тот же Qwen-compatible startup/ACP conformance contract и полный flow.
CLI без `--include-directories` behavior или required ACP capability получает
точную incompatibility error без version probe или provider fallback.

### AC-015 — Safe diagnostics

Traces: REQ-019

Fixtures каждой error category дают различимый provider-neutral cause и Qwen
context, не включая credential, environment secret, полный prompt/response или
запрещённое file content.

### AC-016 — Durable resume

Traces: REQ-020, REQ-021

После закрытия процесса новый Qwen runtime продолжает resumable flow из durable
documents/state/mem-log. В `state.json` и fingerprints отсутствуют Qwen
session/process ID и model choice; reported model metadata не меняет equality.

### AC-017 — Model-selection exclusion

Traces: REQ-021

CLI и UI не содержат Qwen model flag/picker, Stepan не отправляет model-switch
request, а выбранный CLI использует собственную effective model.

### AC-018 — Existing provider regression

Traces: REQ-023

Все существующие Codex и Claude unit, fake, conformance и CLI tests проходят без
изменения наблюдаемого поведения; default `stepan` остаётся Codex.

## Verification approach

- Unit tests CLI parsing, executable resolution, ACP framing/correlation,
  startup argv, capability validation, exact JSON parsing, repair budget, path
  canonicalization, permission decisions и error redaction.
- Replay/fake ACP scenarios для success, malformed events, duplicate IDs,
  invalid envelopes, отсутствующей `additionalDirectories` capability,
  permission races, cancellation и unexpected exit.
- Общий provider-neutral conformance suite для полного `/feature` flow и
  resume.
- Multi-thread integration scenario подтверждает отдельный contained process на
  thread, process-wide видимость только назначенного artifact root и независимое
  закрытие thread lifecycle.
- Process stress tests на Windows/amd64 и native manual lifecycle acceptance на
  Apple Silicon macOS.
- Manual canary matrix для официального Qwen и каждого заявленного стороннего
  CLI: exact executable, tool inventory, artifact write, denied workspace
  write, advisory read boundary, cancel/process cleanup и полный flow.
- Стандартные `go test ./...`, Windows build, macOS/arm64 cross-build и
  закреплённый `actionlint` для изменённых workflow.

## Exclusions

- Выбор модели через новый флаг, UI или ACP model switch.
- Автоматический выбор Qwen или изменение Codex default.
- Установка, обновление, authentication и первоначальная настройка Qwen или
  стороннего CLI средствами Stepan.
- Qwen SDK, `qwen serve`, HTTP/SSE daemon и Claude-compatible transport.
- Обязательный Docker/Podman/Seatbelt sandbox, отдельный security principal,
  disposable worktree или Stepan-controlled filesystem tool bridge.
- Технически гарантированное ограничение нативного чтения Qwen объявленными
  roots.
- Shell, network, MCP, hooks, extensions, skills, memory, subagents и
  background tasks Qwen.
- Version allowlist и vendor/branding certification внутри runtime.
- Qwen-specific Git snapshot вокруг каждого turn и автоматический rollback.
- Linux, Intel Mac, signing и notarization.

## Open questions
