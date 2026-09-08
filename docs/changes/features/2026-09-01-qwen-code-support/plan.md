---
status: draft
---

# План реализации поддержки Qwen Code

## Основание и ограничения

План реализует утверждённый `spec.md` как источник истины. Для каждого logical
thread используется отдельный contained process Qwen и одна ACP-сессия. Старый
review report с незакрытым `SPEC-F-005` не меняет эту топологию.

Единственный вид проверки в плане — автоматизированные test scenarios. Основной
набор использует fake ACP CLI и не зависит от установленного Qwen. Проверки,
которым необходим конкретный Qwen-compatible CLI или поддерживаемое оборудование,
оформляются как opt-in scripts с машинными assertions и не входят в автоматически
запускаемый набор. Задач документации и неавтоматизированных проверок в плане нет.

## Порядок реализации

1. Построить безопасный launcher отдельного Qwen process.
2. Реализовать ACP transport, handshake и preflight state machine.
3. Добавить fail-closed mediation файловых операций.
4. Собрать и валидировать структурированный ответ с ограниченным repair loop.
5. Объединить компоненты в multi-thread runtime с независимым lifecycle.
6. Подключить Qwen к CLI и runtime metadata.
7. Закрепить полный `/feature` flow, resume и Git parity общими автотестами.
8. Добавить opt-in integration scripts для реального CLI и нативного lifecycle.

## TASK-001 — Реализовать contained launcher Qwen process

Traces: REQ-002, REQ-003, REQ-004, REQ-007, REQ-011, REQ-012, REQ-015, REQ-018, REQ-019, REQ-022, DEC-002, DEC-005, DEC-008, DEC-009

### Outcome

Пакет `internal/agentruntime/qwenapp` запускает выбранный executable напрямую
как один изолированно настроенный и process-tree-contained ACP child с
каноническими roots и возвращает контролируемые stdio streams либо безопасную
классифицированную startup error.

### Область и ожидаемые файлы

- `internal/agentruntime/qwenapp/config.go` — immutable process configuration;
- `internal/agentruntime/qwenapp/process.go` — direct child-process lifecycle и
  bounded diagnostics;
- `internal/agentruntime/qwenapp/path.go` — canonical roots и containment checks;
- `internal/agentruntime/qwenapp/process_test.go` и platform-specific process
  tests;
- `arch-go.yml` — разрешённые зависимости нового adapter package.

### Запрещённые области

- изменения поведения `codexapp` или `claudeapp`;
- shell-mediated launch, SDK, HTTP daemon, `qwen serve` или sidecar runtime;
- version, basename, vendor или branding gates;
- запись конфигурации или credentials Qwen в repository;
- изменение CI workflows.

### Шаги реализации

1. Ввести `qwenapp.Config` с executable, Git workspace, process-level JSON
   contract и factory seams для process/job tests.
2. Разрешать официальное PATH-name `qwen` и произвольное авторитетное simple
   executable name без directory separators; отклонять пути, missing PATH entry
   и non-regular resolved target без fallback или version probe.
3. Канонизировать существующий Git root и thread artifact root до запуска;
   отклонять root внутри workspace, sibling roots и link-based escape.
4. Для read-only thread с пустым `ThreadConfig.ArtifactRoot` создавать отдельный
   пустой runtime-owned root вне Git workspace, подключать его process-wide, но
   сохранять write policy пустой; удалять root вместе с thread process.
5. Сформировать фиксированный Qwen-compatible argv: safe mode, approval mode
   `default`, allowlist `read_file`, `write_file`, `edit`, `glob`, `grep_search`,
   явное отключение остальных built-in/synthetic/background возможностей,
   ровно один `--include-directories <artifact-root>` и ACP mode.
6. Передать общий JSON-only contract через process-level system-prompt option,
   Git root через `exec.Cmd.Dir` и сохранить необходимое authentication
   environment без загрузки ambient project/user customizations.
7. Создать и подготовить отдельный `processjob.Job` до handshake, назначить child
   в Job Object/process group сразу после start и закрыть partial process при
   любой ошибке подготовки или назначения.
8. Ограничить stderr diagnostics по размеру, редактировать чувствительные
   значения и реализовать идемпотентные `Wait`/`Close` без оставшихся descendants.

### Локальные технические детали

- Argv строится как slice и передаётся `exec.Command` без shell quoting.
- Runtime-owned root для read-only thread является transport root, но не
  превращается в writable `ThreadConfig.ArtifactRoot`.
- Safe profile задаётся каждым process и не полагается на persisted Qwen
  settings; явно выбранный сторонний CLI обязан принимать тот же launch contract.
- Process wrapper не интерпретирует ACP и не публикует thread handle до
  назначения supervisor.

### Test scenario — Launcher передаёт точный изолированный startup contract

Traces: AC-002, AC-004, AC-007, AC-014

Setup: fake executable записывает argv, environment, working directory и PID,
а workspace и внешний artifact root содержат контролируемые symlink/junction
fixtures. Action: launcher стартует writable thread и отдельно read-only thread.
Expected result: оба запуска direct; argv содержит ровно один root и полный safe
profile без version probe; `cwd` равен Git root; writable process видит только
назначенный root, read-only process получает отдельный пустой runtime-owned root,
а invalid или escaping roots отклоняются до запуска.

### Test scenario — Startup failure не оставляет process tree и секреты

Traces: AC-002, AC-015

Setup: fake job/process factories последовательно отказывают при preparation,
assignment и ACP-child startup, а stderr содержит marker credential и oversized
payload. Action: каждый failure path закрывается launcher-ом. Expected result:
child и descendants отсутствуют, runtime-owned root удалён, error различает
startup/containment cause и не содержит credential или полный stderr body.

## TASK-002 — Реализовать строгий ACP transport и preflight

Traces: REQ-003, REQ-004, REQ-006, REQ-007, REQ-011, REQ-017, REQ-018, REQ-019, DEC-002, DEC-009

Depends-on: TASK-001

### Outcome

Один Qwen child проходит детерминированный ACP initialize/session preflight и
получает строгую correlated JSON-RPC/NDJSON connection, которая fail-closed
завершает thread при несовместимом или неоднозначном wire behavior.

### Область и ожидаемые файлы

- `internal/agentruntime/qwenapp/transport.go` — UTF-8 NDJSON framing;
- `internal/agentruntime/qwenapp/protocol.go` — минимальные ACP wire types;
- `internal/agentruntime/qwenapp/connection.go` — correlation и dispatch;
- `internal/agentruntime/qwenapp/preflight.go` — initialize/session checks;
- `internal/agentruntime/qwenapp/transport_test.go`, `connection_test.go` и fake
  ACP fixtures.

### Запрещённые области

- экспорт ACP types через `agentruntime.Runtime`;
- зависимость от ACP `additionalDirectories`;
- model turn до завершения preflight;
- fallback на Codex, Claude, другой executable или расширенный tool profile;
- tolerance неизвестных terminal/content messages.

### Шаги реализации

1. Реализовать bounded line decoder и serialized writer для одного UTF-8 JSON
   object на NDJSON line с typed string/integer request IDs.
2. Коррелировать local и remote requests отдельно; отклонять duplicate ID,
   orphan/duplicate response, malformed envelope и trailing transport data.
3. Выполнить ACP initialize с клиентской identity Stepan и проверить обязательные
   lifecycle, prompt, cancel и permission-mediation capabilities без version gate.
4. Создать ровно одну session с каноническим Git root в `cwd`; не передавать
   `additionalDirectories`, поскольку artifact root уже подключён при startup.
5. Проверить отсутствие противоречащего safe profile tool inventory, если agent
   предоставляет inventory/status extension; отсутствие необязательной
   `additionalDirectories` capability не считать ошибкой.
6. Запустить reader loop только для созданной session и маршрутизировать prompt
   responses, updates, permission requests и cancel acknowledgements по request
   ID и session ID.
7. При protocol corruption закрыть pending calls, пометить connection unhealthy
   и передать process owner-у классифицированную provider-neutral cause.
8. Формировать diagnostics только из method/capability names и bounded sanitized
   provider context, не включая raw prompt/response или environment.

### Локальные технические детали

- Один connection принадлежит одному process и одной ACP session; foreign
  session ID поэтому однозначно локализует нарушение в thread.
- Terminal barrier и assistant content state передаются response assembler-у из
  TASK-004, но transport сам гарантирует wire order.
- Необъявленные обязательные возможности и явно расширенный inventory являются
  incompatibility; отсутствие non-required extensions допустимо.

### Test scenario — ACP handshake работает без additionalDirectories

Traces: AC-002, AC-004, AC-014

Setup: fake ACP agent принимает startup root, объявляет обязательный lifecycle,
но не объявляет `additionalDirectories`. Action: connection выполняет initialize
и `session/new`. Expected result: session создаётся до model turn, `cwd` равен Git
root, request не содержит `additionalDirectories`, а отсутствие startup-root
behavior или обязательной capability завершает preflight и закрывает process.

### Test scenario — Correlation нарушения закрываются fail-closed

Traces: AC-005, AC-009, AC-013

Setup: replay fixtures содержат duplicate terminal response, orphan response,
foreign session update, terminal response при pending permission и malformed
UTF-8/NDJSON. Action: connection проигрывает каждый stream. Expected result:
соответствующий thread становится unhealthy, pending operations получают одну
protocol error, поздние события не достигают domain state и process закрывается.

### Test scenario — Tool-profile несовместимость выявляется до рабочего хода

Traces: AC-007, AC-014

Setup: fake agents сообщают exact five-tool inventory, missing required file tool
и inventory с shell/web/agent tool. Action: выполняется preflight. Expected result:
точный inventory принимается, остальные варианты получают различимую
incompatibility error без model prompt, version probe или provider fallback.

## TASK-003 — Добавить ACP mediation файловых операций

Traces: REQ-012, REQ-013, REQ-017, REQ-019, DEC-005, DEC-006

Depends-on: TASK-002

### Outcome

Qwen connection разрешает любое число различных корректно correlated write/edit
requests внутри writable artifact root текущего turn, выдавая каждому отдельное
одноразовое решение, и fail-closed отклоняет duplicate/stale/foreign либо иначе
невалидные write и делегированные read requests.

### Область и ожидаемые файлы

- `internal/agentruntime/qwenapp/permissions.go` — active-turn request policy;
- `internal/agentruntime/qwenapp/path.go` — дополнение безопасным разрешением
  существующих и ещё не созданных targets;
- `internal/agentruntime/qwenapp/permissions_test.go` и path fixtures;
- общие provider-conformance path cases в
  `internal/agentruntime/conformance/conformance.go`.

### Запрещённые области

- разрешение workspace, sibling artifact или внешней записи;
- persistent, session-wide или process-wide grants;
- утверждение OS-level/sandboxed isolation для нативного чтения;
- замена native Qwen file tools Stepan-controlled tool bridge;
- Qwen-specific Git snapshot или rollback.

### Шаги реализации

1. Создавать immutable permission context на начало prompt из session ID, turn
   request ID, writable root и разрешённых logical read roots.
2. Принимать только известные write/edit permission methods и требовать
   однозначные session/turn/tool IDs и единственное распознаваемое path field.
3. Разрешать relative paths от Git `cwd`, канонизировать existing target либо
   ближайшего existing ancestor для нового target и проверять symlink/junction
   escape после canonicalization.
4. Разрешать любое число различных write/edit requests только внутри непустого
   artifact root текущего thread; для каждого request возвращать отдельный
   одноразовый turn-scoped approval без сохранения grant.
5. Отклонять Git workspace, sibling root, внешний path, stale/duplicate/foreign
   request, ambiguous fields и запрос расширения срока полномочий.
6. Для делегированного `fs/read_text_file` разрешать только Git root или текущий
   artifact root; нативные read/glob/grep оставлять advisory boundary из prompt.
7. При cancel/close атомарно закрывать все pending permissions отказом и запрещать
   последующую запись по late approval request.
8. Возвращать модели краткий denial result, а вызывающей стороне — безопасную
   provider-neutral classification без раскрытия содержимого файла.

### Локальные технические детали

- Проверка containment использует `filepath.Rel` после разрешения links и
  учитывает platform case/path semantics.
- Наличие transport root у read-only thread не меняет пустой writable root.
- Permission registry удаляет запись после первого ответа независимо от allow
  или deny, поэтому duplicate request не наследует решение.

### Test scenario — Write approval связан с активным turn и artifact root

Traces: AC-004, AC-009

Setup: два Qwen threads имеют разные external roots; active turn содержит две
различные valid write/edit operations, а fixtures также содержат duplicate,
stale, foreign-session, persistent-grant, ambiguous-path, workspace, sibling и
link-escape requests. Action: policy обрабатывает requests на активном и закрытом
turns. Expected result: обе различные корректные операции получают собственные
одноразовые approvals; duplicate и все остальные невалидные requests отклонены и
не расширяют последующие turns.

### Test scenario — Минимальная read posture не выдаётся за sandbox

Traces: AC-008

Setup: bootstrap содержит только канонические Git/artifact roots, а fake agent
посылает delegated reads внутри и вне них и native-read status events. Action:
permission handler обрабатывает delegated requests. Expected result: внутренние
reads разрешены, внешние отклонены, а native events не создают утверждения о
техническом предотвращении чтения вне roots.

## TASK-004 — Реализовать structured response и ограниченный repair loop

Traces: REQ-008, REQ-009, REQ-010, REQ-017, REQ-019, DEC-003, DEC-004

Depends-on: TASK-002, TASK-003

### Outcome

Каждый `RunTurn` возвращает ровно один JSON object, прошедший immutable thread
schema, либо после максимум трёх ACP prompt responses возвращает безопасную
protocol error без изменения domain state.

### Область и ожидаемые файлы

- `internal/agentruntime/qwenapp/prompt.go` — common/first-turn/repair prompts;
- `internal/agentruntime/qwenapp/response.go` — per-prompt assistant assembler;
- `internal/agentruntime/qwenapp/turn.go` — prompt attempts и validation;
- `internal/agentruntime/qwenapp/response_test.go` и `turn_test.go`.

### Запрещённые области

- native Qwen `--json-schema`;
- Markdown extraction, best-effort JSON substring или trailing-data tolerance;
- repair после permission, cancellation, process, correlation или transport
  failure;
- четвёртый hidden response attempt;
- изменение schema после `StartThread`.

### Шаги реализации

1. Добавить к первому session prompt immutable role bootstrap, exact serialized
   schema и session context; последующие ordinary prompts не повторяют bootstrap.
2. Для каждого initial/repair prompt создать новый response assembler, связанный
   с exact session и prompt request ID.
3. В wire order конкатенировать только textual `agent_message_chunk`; игнорировать
   thought, tool и progress updates как candidate payload.
4. Закрывать buffer terminal `session/prompt` response и отклонять второй
   assistant message, неизвестный content type, duplicate terminal и chunk после
   terminal как protocol violation.
5. Требовать один UTF-8 JSON object без prefix/suffix/trailing data и вызвать
   `agentruntime.ValidateOutput` с cloned immutable thread schema.
6. После format/schema error отправить в той же session краткий repair prompt с
   redacted diagnostic и точной schema, начиная новый пустой buffer.
7. Ограничить цикл тремя responses всего: initial плюс не более двух repairs;
   после третьего invalid response вернуть classified repair-exhausted error.
8. Немедленно завершать без repair при cancellation, permission violation,
   process exit, transport corruption и correlation error.

### Локальные технические детали

- Attempt budget равен существующей семантике `DefaultRetryLimit = 3`, но не
  сохраняется в durable state.
- Raw response используется только в bounded in-memory buffer и не включается в
  diagnostics.
- Successful repair остаётся частью исходного user turn и той же ACP session.

### Test scenario — Fragmented JSON становится одним валидным envelope

Traces: AC-005

Setup: fake ACP stream чередует fragmented `agent_message_chunk` с thought,
tool и progress updates и завершает prompt один раз. Action: assembler закрывает
buffer на terminal response. Expected result: только ordered assistant text
образует exact JSON, schema validation проходит, progress не попадает в payload;
Markdown, prefix/suffix, second message, unknown content и late chunk отклоняются.

### Test scenario — Repair имеет ровно три response attempts

Traces: AC-006

Setup: одна session последовательно отдаёт два invalid и один valid response, а
другая — три invalid responses. Action: `RunTurn` выполняет format repair.
Expected result: первая ветка возвращает valid envelope из третьего buffer; вторая
не отправляет четвёртый prompt, возвращает repair-exhausted protocol error и не
публикует ни один invalid payload.

### Test scenario — Первый prompt фиксирует bootstrap и immutable schema

Traces: REQ-008

Setup: записывающий fake ACP agent сохраняет process-level system contract и все
session prompts, а caller после `StartThread` мутирует исходный schema buffer.
Action: thread выполняет первый ordinary prompt, repair prompt и следующий
ordinary prompt. Expected result: process-level contract требует ровно один JSON
object; только первый session prompt содержит exact role instructions, cloned
исходную schema и session context; repair prompt содержит ту же exact schema;
следующий ordinary prompt не повторяет bootstrap, а caller mutation не меняет ни
один отправленный schema fragment.

## TASK-005 — Собрать независимый multi-thread Qwen runtime

Traces: REQ-006, REQ-007, REQ-015, REQ-016, REQ-017, REQ-019, REQ-022, DEC-007, DEC-008

Depends-on: TASK-001, TASK-002, TASK-003, TASK-004

### Outcome

`qwenapp.Runtime` реализует provider-neutral runtime contract с одним contained
process/session на logical thread, независимым `CloseThread`, локализацией
thread failures и глобальными `Interrupt`/`Close` guarantees.

### Область и ожидаемые файлы

- `internal/agentruntime/qwenapp/runtime.go` и `thread.go`;
- `internal/agentruntime/qwenapp/runtime_test.go`, lifecycle и stress fixtures;
- `internal/agentruntime/runtime.go` — provider-neutral error classification без
  изменения методов interface;
- `internal/specflow/session.go` и `session_registry.go` — различение thread-local
  и runtime-wide failures;
- соответствующие `internal/specflow/*_test.go`.

### Запрещённые области

- shared Qwen process между logical threads;
- durable process/session/request IDs;
- изменение сигнатур `StartThread`, `RunTurn`, `CloseThread`, `Interrupt`, `Close`;
- влияние локализованной ошибки одного thread на sibling process;
- продолжение process activity после close checkpoint.

### Шаги реализации

1. Хранить runtime-owned map opaque handles; каждый `StartThread` clone-ит config,
   создаёт launcher/connection/session и публикует handle только после preflight.
2. Сохранить последовательную provider-neutral семантику turns и возвращать
   `ErrTurnInProgress` при конкурентном вызове того же активного runtime path.
3. Привязать active turn, permissions и response assembler к одному thread и
   помечать unhealthy только process, которому однозначно принадлежит violation.
4. Ввести provider-neutral различение thread-local protocol/runtime failure и
   общей runtime failure; `Session` не discard-ит весь Qwen runtime при
   локализованной ошибке, а `SessionRegistry` удаляет повреждённый role handle.
5. Реализовать `CloseThread` как инвалидизацию одного handle, fail-closed closure
   его approvals/connection/process/job и удаление runtime-owned temporary root.
6. При `Interrupt` active turn сначала отправить `session/cancel`, закрыть pending
   approvals и ждать terminal path не более трёх секунд, затем закрыть все
   process trees и инвалидировать все handles.
7. Если active turn отсутствует, пропустить cancel и всё равно закрыть handles и
   trees; сделать runtime `Close` идемпотентным и безусловно глобальным.
8. Отбрасывать late events после close checkpoint; не принимать их новым runtime
   и не позволять им завершать новый request с совпавшим provider ID.
9. Сохранить незатронутые threads после локальной startup/protocol failure; если
   owner установить нельзя, закрыть runtime целиком fail-closed.

### Локальные технические детали

- Process count является производным от live handle count и проверяется через
  injectable process factory.
- Handle включает runtime identity и generation, поэтому чужой, stale или
  закрытый handle не может совпасть с новой session.
- Close checkpoint устанавливается до release ресурсов, чтобы reader goroutine
  не публиковала события во время teardown.

### Test scenario — Два threads владеют двумя независимыми process trees

Traces: AC-002, AC-011, AC-012

Setup: runtime получает fake launcher, создающий два separately supervised ACP
processes с разными session/root records. Action: создаются два handles, затем
первый закрывается и второй выполняет turn. Expected result: до close существуют
два process trees; после `CloseThread` первого остаётся ровно второе дерево и
вторая session успешно отвечает; failure нового supervisor не затрагивает уже
открытый handle.

### Test scenario — Interrupt соблюдает cancel grace и закрывает всё

Traces: AC-013

Setup: fake active prompt имеет pending permission и варианты acknowledge/ignore
cancel; clock и grace timer управляются тестом. Action: вызывается `Interrupt`.
Expected result: `session/cancel` отправляется только active session, approvals
закрываются, ожидание не превышает три секунды, затем все trees исчезают, handles
невалидны, а late events не меняют state.

### Test scenario — Локализованная ошибка сохраняет sibling thread

Traces: AC-011, AC-015

Setup: два threads открыты, первый получает foreign/duplicate protocol event, а
второй имеет valid pending script. Action: первый `RunTurn` завершается ошибкой и
registry запрашивает второй. Expected result: закрыт только первый process,
повреждённый role handle удалён, второй возвращает valid envelope; diagnostics
различает protocol cause и не содержит raw bodies или secrets.

### Test scenario — Все Qwen errors имеют безопасную классификацию

Traces: AC-015

Setup: diagnostic matrix создаёт отдельные fake fixtures для missing и
non-regular executable, handshake/capability incompatibility, process exit,
protocol violation, permission denial, repair exhaustion и operator cancellation;
каждая fixture внедряет credential, environment secret, полный prompt/response и
запрещённое file content. Action: runtime выполняет соответствующий failure path
и сериализует user-facing error. Expected result: каждая ветка имеет различимый
provider-neutral cause и только допустимый Qwen context — provider, выбранный
executable либо capability name; ни одно запрещённое значение не присутствует.

## TASK-006 — Подключить Qwen к CLI и runtime metadata

Traces: REQ-001, REQ-002, REQ-003, REQ-018, REQ-019, REQ-021, REQ-023, DEC-001, DEC-009, DEC-011

Depends-on: TASK-005

### Outcome

`cmd/stepan` явно выбирает Qwen через закрытый enum, разрешает правильный
executable, создаёт `qwenapp.Runtime` без fallback и передаёт provider `qwen` с
model metadata `default`, сохраняя Codex default и переводя Claude на общий
optional official/custom PATH-name contract.

### Область и ожидаемые файлы

- `cmd/stepan/agent_config.go` и `agent_config_test.go`;
- `cmd/stepan/main.go` и `main_test.go`;
- `internal/agentruntime` — shared executable name validation/PATH resolution;
- `internal/agentruntime/qwenapp` public constructor/config seam;
- `arch-go.yml` — dependency rule composition root → `qwenapp`.

### Запрещённые области

- Qwen как default или автоматическое provider detection;
- обязательный `--agent-cli-name` для любого provider;
- абсолютный path или directory separators в `--agent-cli-name`;
- model flag, picker или ACP model-switch request;
- сохранение model/process/session metadata в durable state.

### Шаги реализации

1. Добавить `agentQwen` в закрытый parser enum, usage и unknown-provider
   diagnostic; оставить `agentCodex` default.
2. Без `--agent-cli-name` передавать официальное PATH-name выбранного provider:
   `codex`, `claude` или `qwen`; supplied value принимать только как simple
   non-empty executable name без surrounding whitespace, absolute path и directory separators;
   отличать omitted flag от явно пустого значения, которое является usage error.
3. Разрешать точное имя через `PATH` в provider process/client layer, проверять
   resolved regular file, сохранять отсутствие basename/branding/version gate и
   не заменять missing или несовместимый executable другим provider-ом.
4. Добавить отдельную ветку `runtimeFactory`, создающую Qwen runtime с workspace,
   common system contract и envelope schema.
5. Передать `RuntimeIdentity{Provider: "qwen", Model: "default"}` в application;
   не добавлять model selection в CLI/UI и не включать metadata в fingerprints.
6. Классифицировать Qwen startup/preflight errors до входа в flow, сохраняя
   provider/executable/capability names и redaction rules adapter-а.
7. Оставить Codex default и observable provider argv без изменения; перевести
   Claude на optional official/custom PATH-name с передачей SDK уже разрешённого
   абсолютного executable и сохранить отсутствие provider fallback.

### Локальные технические детали

- PATH resolution выполняется process layer-ом, чтобы найденный executable был
  каноническим до запуска; parser хранит только user selection.
- Сообщённая ACP model identity может использовать существующий metadata slot
  позднее, но её отсутствие и значение не меняют state equality.

### Test scenario — CLI выбирает только запрошенный provider

Traces: AC-001, AC-014

Setup: composition tests предоставляют official и compatible fake PATH
executables с нестандартными именами. Action: разбираются defaults всех providers,
Qwen custom name, explicit empty/whitespace, absolute/separator/missing values и
unknown-agent invocations.
Expected result: default создаёт Codex, valid cases запускают exact selected
PATH-name, invalid cases возвращают usage/configuration error и ни один случай не
запускает fallback или version/branding probe.

### Test scenario — Qwen не добавляет model selection или durable identity

Traces: AC-017

Setup: CLI/UI schemas, runtime factory spy и serialized state/fingerprint fixtures
проходят с provider Qwen. Action: создаётся application и сохраняется flow state.
Expected result: model flag/picker/switch request отсутствуют, runtime context
содержит `qwen` и `default`, а serialized state и fingerprint не содержат model,
process или session identity.

## TASK-007 — Закрепить provider parity, Git policy и durable resume

Traces: REQ-005, REQ-014, REQ-020, REQ-021, REQ-023, DEC-010

Depends-on: TASK-005, TASK-006

### Outcome

Общие automated suites проводят Qwen fake adapter через полный `/feature` flow,
Git gates и restart/resume с той же domain semantics, что Codex и Claude, и
защищают существующих providers от регрессии.

### Область и ожидаемые файлы

- `internal/agentruntime/conformance/conformance.go` — расширенные shared suites;
- `internal/agentruntime/qwenapp/conformance_test.go` и fake executable harness;
- `internal/specflow/application_test.go`, `session_test.go`, `resume_test.go`;
- `cmd/stepan/main_test.go` — composition-level flow cases;
- существующие Codex/Claude conformance tests только для подключения общего suite.

### Запрещённые области

- provider branches в `FeatureController`, `StageEngine` или repository;
- Qwen-specific per-turn Git snapshot и automatic rollback;
- provider/session/model fields в `state.json` или fingerprints;
- изменение document contracts, stage transitions или command availability;
- реальный CLI dependency в основном test suite.

### Шаги реализации

1. Расширить provider-neutral conformance suite author/reviewer threads,
   decisions, artifact publication, rework, approval, close и fresh resume.
2. Подключить Qwen fake process/ACP scripts к тому же suite, оставив decode и
   writable-root policy за adapter fixture.
3. Провести composition-level flow через intent, spec и plan dialogues, review,
   automatic rework, material decision, approvals и completion.
4. Проверить clean repository preflight и общие pre-approval blockers без
   snapshot вокруг Qwen turns и без rollback изменённых файлов.
5. Закрыть process/runtime, восстановить feature новым Qwen runtime из documents,
   reports, state и mem-log и создать fresh provider sessions.
6. Проверить отсутствие Qwen IDs, prompts, capability payloads и model metadata в
   state/fingerprints при допустимом provider/model front matter review reports.
7. Запустить shared adapter scenarios для Codex и Claude без изменения их
   expected outputs, argv, permissions и lifecycle semantics.
8. Сохранить domain packages независимыми от `qwenapp` и обновить architecture
   assertions только для composition dependency.

### Локальные технические детали

- Fake Qwen является настоящим child process, говорящим ACP по stdio; domain
  runner не подменяется in-memory mock-ом в Qwen composition scenario.
- Resume никогда не пытается восстановить Qwen session ID и начинает новый
  process для каждого заново созданного logical role thread.

### Test scenario — Один shared flow проходит с тремя adapters

Traces: AC-003, AC-018

Setup: Codex, Claude и Qwen factories получают эквивалентные scripted domain
outcomes и external artifact roots. Action: shared suite выполняет author/reviewer
dialogue, decision, artifact, rework, approval, close и resume. Expected result:
все три adapters дают одинаковые domain transitions и command availability;
default Codex остаётся прежним, а Claude использует новый общий optional
official/custom PATH-name contract.

### Test scenario — Qwen использует общие Git gates без rollback

Traces: AC-010

Setup: temporary Git repositories содержат clean create case и изменения внутри
и вне текущей feature перед approval. Action: полный Qwen-backed fake flow
достигает create и approval boundaries. Expected result: применяются те же
blockers, что Codex/Claude; Qwen-specific snapshot отсутствует, blocked files
сохраняются и rollback не выполняется.

### Test scenario — Resume восстанавливает только durable flow context

Traces: AC-016

Setup: Qwen-backed flow сохраняет published documents, review history, state и
mem-log, затем process/runtime закрываются. Action: новый application instance
выполняет resume и продолжает следующий role turn. Expected result: создаются
fresh Qwen process/session, flow продолжается с устойчивого состояния, а
state/fingerprints не содержат прежние process/session/model данные.

## TASK-008 — Добавить opt-in real-CLI и platform integration scripts

Traces: REQ-002, REQ-003, REQ-004, REQ-005, REQ-007, REQ-011, REQ-012, REQ-013, REQ-015, REQ-016, REQ-018, REQ-019, REQ-022, DEC-005, DEC-006, DEC-007, DEC-008, DEC-009

Depends-on: TASK-005, TASK-006, TASK-007

### Outcome

Явно запускаемые automated scripts проверяют официальный или сторонний
Qwen-compatible CLI на Windows/amd64 и Apple Silicon macOS/arm64, формируют
machine-readable result и не выполняются обычным test target или CI workflow.

### Область и ожидаемые файлы

- `scripts/test-qwen-conformance.ps1` — Windows opt-in runner;
- `scripts/test-qwen-conformance.sh` — macOS opt-in runner;
- `internal/agentruntime/qwenapp/real_cli_integration_test.go` — общий harness с
  explicit opt-in gate;
- `internal/agentruntime/qwenapp/real_cli_windows_test.go` и
  `real_cli_darwin_test.go` — platform lifecycle assertions;
- test-only structured result types/fixtures внутри `qwenapp`.

### Запрещённые области

- включение scripts в default `go test` path или GitHub Actions;
- скачивание, установка, authentication или обновление CLI;
- acceptance по basename, branding или version string;
- запись за пределы временных Git/artifact fixtures;
- human-evaluated pass/fail или отдельный Markdown test plan.

### Шаги реализации

1. Требовать explicit executable NAME, заранее установленное пользователем в
   `PATH`, и opt-in marker; без них test binary завершает scenario как not selected,
   не разыскивая и не устанавливая CLI. Пути и directory separators отклоняются.
2. Создавать disposable Git workspace, два external artifact roots, read canary
   вне logical roots и machine-readable result location внутри test temp area.
3. Проверять exact startup profile, ACP initialize/session/prompt/cancel,
   отсутствие зависимости от `additionalDirectories` и два process/session roots.
4. Запрашивать inventory и поведенческие probes пяти разрешённых tools; требовать
   отсутствие либо отказ shell, web, MCP, hooks, extensions, skills, memory,
   subagents и background tasks.
5. Проверять valid artifact write, отказ workspace/sibling/link-escape write и
   correlation одноразового approval.
6. Выполнить advisory native-read canary и записать фактический результат с
   обязательной меткой отсутствия OS-level isolation; delegated external read
   должен быть отклонён машинным assertion.
7. На Windows проверить отдельные Job Objects, независимый `CloseThread`, global
   interrupt/close и отсутствие descendants/late writes после checkpoint.
8. На физическом Apple Silicon Mac проверить отдельные process groups и те же
   lifecycle assertions; Windows script отдельно подтверждает macOS/arm64
   cross-compilation test target без объявления runtime success.
9. Для exact explicit PATH-name с нестандартным именем прогнать полный
   `/feature` flow: author/reviewer dialogues, material decision, automatic
   rework, approvals, close и fresh resume; сохранить только sanitized
   machine-readable summary.
10. Сделать scripts одинаковыми для официального и нестандартно названного
    стороннего executable: различие определяется только startup/ACP/tool behavior.

### Локальные технические детали

- Integration tests исключены из default build/test selection build tag или
  эквивалентным explicit gate; scripts являются единственной точкой opt-in.
- Каждый assertion имеет bounded timeout и cleanup, чтобы failed CLI не оставлял
  process tree или temporary data.
- Native-read result является наблюдением, а не sandbox PASS; pass/fail теста не
  требует технического запрета нативного чтения вне roots.

### Test scenario — Реальный сторонний CLI проходит contract и полный flow

Traces: AC-003, AC-007, AC-008, AC-014

Setup: runner получает exact explicit official либо third-party Qwen-compatible
executable NAME, уже доступное в `PATH`; third-party fixture имеет нестандартное
имя, а harness создаёт isolated test fixtures. Action: opt-in harness выполняет
ACP, inventory,
file-policy и native-read probes, затем проходит intent/spec/plan author и
reviewer dialogues, material decision, automatic rework, approvals, close и
fresh resume. Expected result: exact five-tool surface и startup contract
подтверждены, запрещённые operations не исполняются, полный flow завершается с
той же domain semantics, advisory read observation маркировано корректно, а имя
executable не вызывает branding/version probe или provider fallback.

### Test scenario — Windows native lifecycle закрывает нужные деревья

Traces: AC-011, AC-013

Setup: Windows/amd64 runner создаёт два real-CLI threads и фиксирует PID trees и
artifact timestamps. Action: закрывается первый thread, затем вызываются cancel,
interrupt и runtime close для оставшегося. Expected result: Job Objects разделены,
первое закрытие сохраняет второй thread, global operations удаляют все trees не
позже cancel grace, а после checkpoints отсутствуют late events и file changes.

### Test scenario — macOS native lifecycle и cross-build разделены

Traces: AC-012

Setup: macOS/arm64 runner работает на физическом Apple Silicon host, а отдельная
cross-build fixture использует тот же исходный test target без запуска binary.
Action: native harness создаёт два process groups и выполняет thread/global close;
cross-build fixture компилирует target. Expected result: каждая group независимо
контролируется и после checkpoints отсутствует вместе с descendants; cross-build
сообщает только compile result и не подменяет native runtime outcome.

## Open questions
