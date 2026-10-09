---
status: draft
---

# Stepan: implementation plan for intent → spec → plan

## Основание и ограничения

План реализует `spec.md` этой feature без изменения зафиксированных в ней
материальных решений. Задачи организованы вокруг глубоких модулей
`FeatureController`, `StageEngine`, `FeatureRepository` и `PromptCatalog`.
Инфраструктурные детали остаются скрыты за их интерфейсами, а UI не дублирует
правила переходов.

Единственный вид проверки в этом плане — автоматизированные тестовые сценарии.
В плане нет ручной приёмки, отдельных проверочных команд, задач документации
или самостоятельных проверок, не выраженных автотестами.

## Порядок реализации

1. Ввести доменные контракты, prompt composition и parser документов.
2. Реализовать durable-операции feature и Git-транзакции.
3. Построить общий author lifecycle и review/rework lifecycle.
4. Собрать переходы в `FeatureController`, затем добавить recovery и resume.
5. Подключить UI, provider adapters и composition root.
6. Удалить заменённый intent-only lifecycle и закрепить полный flow
   интеграционными автотестами.

## TASK-001 — Ввести доменную модель planning flow

Traces: REQ-001, REQ-005, REQ-018, REQ-019, REQ-023, REQ-036, REQ-037, REQ-038, REQ-048, DEC-001, DEC-003, DEC-006, DEC-010, DEC-012

### Outcome

Пакет `internal/specflow` имеет единый набор типов для стадий, ролей, статусов,
review findings, fingerprints, retry counters, stable IDs, provider-neutral
envelope и `Progress`. Недопустимые значения и переходы нельзя незаметно
представить через прежний плоский intent-only enum.

### Область и ожидаемые файлы

- `internal/specflow/contracts.go` — provider-neutral turn envelope и его
  нормализация;
- `internal/specflow/state.go` — `FlowState`, состояние стадий и review;
- `internal/specflow/model.go` — stage/role/finding/fingerprint value objects;
- `internal/specflow/progress.go` — `Progress` и `CommandHint`;
- `internal/specflow/*_test.go` — table-driven domain tests.

### Запрещённые области

- provider-specific transport code;
- filesystem и Git operations;
- UI rendering;
- физические prompt assets.

### Шаги реализации

1. Заменить intent-only состояния независимыми `flow_status`, `current_stage`,
   per-stage status и per-stage review status.
2. Зафиксировать допустимые стадии `intent`, `spec`, `plan`, роли author/reviewer
   и наборы статусов из specification.
3. Представить current/approved hashes, upstream hashes, `outdated`, issued IDs,
   retry counters, supersession links и review metadata в versioned state.
4. Исключить из state commit SHA, operation ID, provider thread handle и prompt
   metadata на уровне сериализуемого контракта.
5. Ввести immutable identity finding и отдельно изменяемые decision/status поля;
   запретить смену severity и dismissal contract violation.
6. Нормализовать transport envelopes `message | artifact`: пустой artifact
   message не становится доменным сообщением, artifact path отсутствует.
7. Добавить в `Progress` готовый ordered список `CommandHint`, не связывая
   доменную модель с terminal rendering.
8. Оставить изменение `FlowState` доступным только orchestration-коду
   `FeatureController` и операциям восстановления `FeatureRepository`.

### Task-local технические детали

- Значения статусов сериализуются строками, совпадающими со specification.
- Fingerprint является value object из target hash и ordered map upstream
  hashes; provider/model/session в equality не входят.
- Stable ID хранится в канонической форме с числовым suffix без leading zeros,
  а исходное spelling остаётся свойством разобранного документа.
- Решение finding хранит `Decision`, `Decided-by` и `Rationale` независимо от
  lifecycle status finding.

### Test scenario — Независимые оси состояния сериализуются без потери данных

Traces: AC-014, AC-021

Table-driven тест создаёт состояния со всеми допустимыми stage/review status,
сериализует и читает их обратно. Он ожидает сохранения hashes, issued IDs,
retry counters, review metadata и supersession links, а также отсутствия
полей commit SHA, thread handle и prompt metadata.

### Test scenario — Невалидные доменные значения отвергаются интерфейсом

Traces: AC-021, AC-023

Тесты через публичные constructors пытаются создать нулевой stable ID,
неизвестный status, finding с изменённой severity, dismissed contract finding
и material finding с решением reviewer-а. Каждый случай возвращает доменную
ошибку и не создаёт изменённое состояние.

### Test scenario — Envelope имеет одинаковую доменную семантику

Traces: AC-013

Тест нормализует `message` и `artifact` envelopes, проверяет обязательность
всех transport properties, удаление пустого artifact message и невозможность
передать artifact path через ответ agent-а.

## TASK-002 — Реализовать `PromptCatalog` и embedded defaults

Traces: REQ-027, REQ-028, REQ-029, REQ-030, REQ-031, REQ-032, REQ-033, REQ-034, REQ-035, REQ-036, DEC-005, DEC-010, DEC-011

Depends-on: TASK-001

### Outcome

Control plane получает effective prompt каждой роли через один
`PromptCatalog`. Immutable system contracts, role prompts, capabilities и
runtime context собираются в зафиксированном порядке из embedded defaults;
callers не знают физических путей prompt fragments.

### Область и ожидаемые файлы

- `internal/specflow/prompt_catalog.go` — interface, logical IDs и composition;
- `internal/specflow/prompt_catalog_embedded.go` — default adapter;
- `internal/specflow/prompts/system/*.md`;
- `internal/specflow/prompts/roles/*.md`;
- `internal/specflow/prompts/capabilities/common/*.md`;
- `internal/specflow/prompts/capabilities/{intent,spec,plan}/*.md`;
- `internal/specflow/prompt_catalog_test.go`.

### Запрещённые области

- user-configurable prompt overrides;
- сохранение prompt IDs, sources или hashes в feature artifacts;
- выбор capability composition в UI или provider adapters;
- documentation roles и documentation stage.

### Шаги реализации

1. Определить `PromptCatalog` как seam разрешения logical ID и построения
   effective prompt для конкретной role.
2. Валидировать lowercase ASCII POSIX IDs без расширения, absolute path и
   сегментов `.`/`..`.
3. Встроить system и role prompt для пяти ролей.
4. Встроить общие `capabilities/common/project-context` и
   `capabilities/common/brainstorming`.
5. Встроить специализированные author/review/document capabilities для intent,
   spec и plan.
6. Закодировать ordered role-to-capabilities mapping в control plane.
7. Собирать layers в порядке system contract, role, capabilities, runtime
   context; явно обозначить приоритет system contract в effective text.
8. Перенести требования фиксированного artifact filename и flat envelope в
   immutable system prompt соответствующей роли.
9. Удалить зависимость новых flows от единственного `prompts/bootstrap.md`.

### Task-local технические детали

- Полный capability ID всегда начинается с `capabilities/`; сокращённые имена
  из mapping не выходят за пределы control plane.
- Embedded adapter может хранить fragments как отдельные Markdown assets, но
  interface не раскрывает способ хранения.
- Prompt assets являются runtime-инструкциями feature, а не отдельной стадией
  продуктовой документации.
- Runtime context передаётся последним отдельным fragment и не может менять
  system contract или control-plane composition.

### Test scenario — Каждая роль получает точную ordered composition

Traces: AC-012

Table-driven тест вызывает `PromptCatalog` для пяти ролей и ожидает immutable
system prompt, правильный role prompt и capabilities в порядке из REQ-033.
Ни UI, ни runtime не передают собственный список capabilities.

### Test scenario — Невалидные logical prompt IDs не разрешаются

Traces: AC-012

Тесты передают IDs с uppercase, расширением, backslash, absolute prefix,
пустым segment, `.` и `..`. Catalog возвращает typed error; допустимый ID
разрешается независимо от физического embedded path.

### Test scenario — Default document prompts сохраняют машинный контракт

Traces: AC-004, AC-010, AC-011, AC-012

Тест читает effective prompts author/reviewer ролей и подтверждает наличие
обязательных секций документов, stable ID правил, `Traces:`, `Depends-on:`,
`Open questions`, test scenarios и запретов на material decisions без
пользователя.

## TASK-003 — Построить parser и validator машинного контракта документов

Traces: REQ-009, REQ-017, REQ-018, REQ-019, REQ-023, REQ-024, REQ-025, REQ-026, REQ-027, REQ-028, REQ-029, DEC-008, DEC-009

Depends-on: TASK-001

### Outcome

Один parser разбирает intent, spec, plan и review reports, возвращает
структурированное представление и полный набор детерминированных нарушений.
Тот же interface сообщает все увиденные IDs до принятия решения о публикации,
чтобы repository мог навсегда зарезервировать их.

### Область и ожидаемые файлы

- `internal/specflow/document_contract.go` — parser interface и общие errors;
- `internal/specflow/document_ids.go` — heading IDs и canonicalization;
- `internal/specflow/spec_contract.go`;
- `internal/specflow/plan_contract.go`;
- `internal/specflow/review_contract.go`;
- `internal/specflow/document_contract_test.go` и специализированные test files.

### Запрещённые области

- agent runtime calls;
- state transitions и публикация файлов;
- семантические решения вместо пользователя;
- project-level deterministic check configuration.

### Шаги реализации

1. Разобрать Markdown headings любого уровня и распознать все шесть семейств
   ID из REQ-023.
2. Сохранить source spelling и вычислить canonical identity; находить duplicate
   aliases, zero и malformed IDs.
3. Возвращать observed IDs даже при остальных parser errors.
4. Разбирать фиксированные поля `Traces:` и `Depends-on:` и разрешать ссылки
   по канонической identity.
5. Валидировать spec: AC с минимум одним REQ, отсутствие unknown/deleted refs и
   корректный `Open questions` section.
6. Валидировать plan: task traces, dependency graph, отсутствие cycles, полное
   покрытие REQ/DEC/AC и вложенные `Test scenario` headings.
7. Проверять, что каждый test scenario ссылается минимум на AC или REQ, а все
   AC покрыты хотя бы одним test scenario.
8. Валидировать review findings, immutable поля, status-specific metadata,
   decision provenance и отсутствие `Traces` только для whole-document issue.
9. Представлять нарушения как ordered diagnostics, пригодные для parser repair
   loop без создания review report.

### Task-local технические детали

- Parser не меняет padding author-документа; canonicalization применяется для
  identity, equality и ссылок.
- `Open questions` распознаётся по heading любого уровня с точным названием;
  пустой section допустим, строка `None` считается содержимым.
- Test scenario не получает отдельного ID; принадлежность задаётся вложенностью
  под ближайший `TASK-*`.
- Проверка покрытия работает по активным элементам текущих spec и plan, а
  issued-ID registry используется только для запрета повторного использования.

### Test scenario — Spec ID lifecycle и ссылки валидируются полностью

Traces: AC-010

Table-driven тесты принимают разные heading levels, gaps и leading zeros, но
отклоняют zero, duplicate spelling aliases, unknown traces, AC без REQ и ID,
который ранее был удалён. Diagnostics содержат все нарушения одного документа.

### Test scenario — Plan dependency graph и coverage валидируются полностью

Traces: AC-011

Тесты отклоняют task без REQ/DEC trace, unknown dependency, self-dependency,
многошаговый cycle, task без test scenario, scenario без AC/REQ trace и plan с
непокрытым REQ, DEC или AC. Валидный документ возвращает parsed dependency
graph без изменения исходного Markdown.

### Test scenario — Review finding contract сохраняет identity

Traces: AC-009, AC-023

Тест сравнивает две редакции review report: разрешённые status/resolution
updates принимаются, а смена ID, severity, initial problem или location/traces
отклоняется. Dismissed material finding требует user rationale, contract
finding нельзя dismiss, а whole-document finding не требует `Traces`.

### Test scenario — Open questions блокируют только approval

Traces: AC-004

Parser принимает draft с явно отложенным вопросом как публикуемый документ,
но approval validation возвращает blocking diagnostic. После удаления вопроса
и фиксации решения тот же документ проходит approval validation.

## TASK-004 — Реализовать durable `FeatureRepository`

Traces: REQ-006, REQ-008, REQ-010, REQ-014, REQ-017, REQ-024, REQ-037, REQ-038, REQ-039, REQ-040, REQ-042, REQ-043, DEC-004, DEC-007, DEC-008, DEC-012

Depends-on: TASK-001, TASK-003

### Outcome

`FeatureRepository` предоставляет глубокие доменные операции создания и
загрузки feature, публикации draft/review, записи журнала, обновления state,
отбрасывания pending artifact и восстановления частичных filesystem operations.
Controller не координирует отдельные записи файлов.

### Область и ожидаемые файлы

- `internal/specflow/repository.go` — interface доменных операций;
- `internal/specflow/repository_fs.go` — filesystem adapter;
- `internal/specflow/workspace.go` — канонические targets и artifact roots;
- `internal/specflow/journal.go` — append-only typed entries;
- `internal/specflow/repository_test.go` и `workspace_test.go`.

### Запрещённые области

- Git commit orchestration, добавляемая в TASK-005;
- UI formatting;
- provider-specific sessions;
- запись project artifacts непосредственно agent-ом.

### Шаги реализации

1. Определить операции `Create`, `Load`, `PublishAuthorDraft`,
   `PublishReview`, `RecordDecision`, `DiscardPending`, `InspectChanges` и
   `Recover` в терминах feature, а не отдельных файлов.
2. Создавать канонические `intent.md`, `spec.md`, `plan.md`, `mem-log.md`,
   `state.json` и `reviews/` только тогда, когда они становятся применимы.
3. Публиковать Stepan-owned artifacts атомарной заменой после parser validation;
   agent workspace оставлять read-only.
4. Резервировать все observed IDs отдельной durable-операцией сразу после
   появления draft, включая rejected и discarded drafts.
5. Записывать mem-log entries с обязательными stage, role и event kind для
   brief, сообщений, decisions, diff, review events, attempts, errors,
   approvals и commits.
6. Добавлять и обновлять front matter текущего numbered review report, не
   создавая архивный файл на recheck.
7. Хранить pending artifact только во внешнем artifact root; при завершении
   session удалять root и оставлять published revision неизменной.
8. Классифицировать изменения основных документов и обнаруживать изменения
   Stepan-owned state, journal и review artifacts до следующего flow action.
9. При загрузке проверять внутреннюю согласованность state, документов,
   fingerprints и журнала; однозначные частичные writes завершать, иначе
   возвращать blocking diagnostic без перезаписи.

### Task-local технические детали

- Review filename выделяется монотонно отдельно для spec и plan; номер не
  зависит от количества внутренних updates одного запуска.
- `state.json` является versioned Git artifact, но не содержит self-referential
  hash или данные provider thread.
- Journal append выполняется через repository operation вместе с соответствующим
  state/document update, чтобы callers не могли поменять порядок durable writes.
- Изменение `intent.md`, `spec.md` или `plan.md` классифицируется отдельно от
  запрещённого изменения service-owned artifacts.

### Test scenario — Feature round-trip восстанавливает authoritative artifacts

Traces: AC-014, AC-020, AC-021

Filesystem test создаёт feature, публикует документы, decisions и review,
закрывает repository и загружает его новым instance. Ожидаются те же state,
current documents, current review и typed journal entries без зависимости от
in-memory данных.

### Test scenario — Pending revision отбрасывается, а issued IDs сохраняются

Traces: AC-003, AC-010, AC-014

Тест публикует initial revision, принимает новый artifact с новым ID и оставляет
его pending. После session close artifact root отсутствует, опубликованный файл
не изменён, diff/event присутствуют в журнале, а ID остаётся reserved после
повторной загрузки.

### Test scenario — Один review run обновляет один numbered report

Traces: AC-005, AC-009

Тест публикует initial review, material decision и recheck update. Во feature
существует один `reviews/spec-N.md` с актуальными statuses и полным front matter;
новый номер появляется только для нового review run.

### Test scenario — Изменение service-owned artifact блокирует repository

Traces: AC-020

Filesystem test изменяет по очереди `state.json`, `mem-log.md` и текущий review
в обход repository. Следующая domain operation возвращает blocking diagnostic
и не перезаписывает пользовательские bytes; изменение основного документа
возвращается как stage-specific external revision.

## TASK-005 — Добавить phase commits, supersession и Git recovery

Traces: REQ-003, REQ-011, REQ-038, REQ-044, REQ-045, REQ-046, REQ-047, DEC-004, DEC-007, DEC-012

Depends-on: TASK-004

### Outcome

`FeatureRepository` атомарно выполняет approval/revision/supersession operations
вместе с feature-scoped Git commits, не захватывает чужие изменения и
восстанавливает устойчивое состояние после commit failure или process crash.

### Область и ожидаемые файлы

- `internal/specflow/repository.go` — approval и supersession operations;
- `internal/specflow/repository_git.go` — скрытая Git реализация;
- `internal/gitsnapshot/*` — только если требуется расширить существующие
  read-only primitives без утечки Git orchestration в controller;
- `internal/specflow/repository_git_test.go`.

### Запрещённые области

- `FeatureController` как orchestrator отдельных Git commands;
- включение путей вне текущей feature, кроме двух feature directories при
  supersession;
- сохранение commit SHA или отдельного `committing` status;
- изменение пользовательского index ради phase commit.

### Шаги реализации

1. Добавить preflight новой feature: working tree и index полностью чистые.
2. Добавить approval operation, которая валидирует документы/review/open
   questions, проверяет paths вне текущей feature и только затем фиксирует
   committed state и feature-only commit.
3. Использовать точные commit messages для intent, spec, plan и intent revision.
4. При commit error вернуть stage к `published`, записать unsuccessful attempt
   в mem-log и сохранить возможность повторного `/approve`.
5. Реализовать recovery после state=`committed`: clean feature directory
   подтверждает commit, dirty directory откатывает stage к `published`.
6. Завершать известную частичную durable-операцию по hashes только при
   единственном однозначном результате; иначе блокировать без перезаписи.
7. Реализовать supersession как одну repository operation: старая feature
   становится `superseded`, новая получает скопированный изменённый intent,
   обе получают двусторонние связи и входят в один commit.
8. Сохранить старую feature superseded независимо от последующего approval
   нового intent.

### Task-local технические детали

- Cleanliness и path ownership определяются относительно repository root, а не
  process working directory.
- Approval считается состоявшимся только если domain operation завершила
  durable state и Git commit; caller получает один итоговый результат.
- Supersession является единственным operation, которому разрешены два feature
  directories.
- Git audit остаётся в истории repository; state хранит hashes документов, но
  не индекс commit.

### Test scenario — Dirty tree блокирует старт и approval до изменения state

Traces: AC-015, AC-016, AC-021

Integration test в temporary Git repository создаёт staged и unstaged changes
вне feature. Новый flow и `/approve` возвращают все blocking reasons, approval
не записывается и чужие paths не входят в commit.

### Test scenario — Успешный phase commit включает только feature

Traces: AC-001, AC-016

Тест публикует валидный intent, spec и plan последовательными approvals.
Каждый commit имеет требуемое сообщение и содержит только накопленные файлы
текущей feature; следующая стадия открывается только после результата commit.

### Test scenario — Commit failure и crash возвращают устойчивое состояние

Traces: AC-016, AC-021

Fault-injection тест прерывает operation до Git commit и эмулирует commit
error. Повторная загрузка различает clean/dirty feature directory, возвращает
stage в `published` при незавершённом commit, сохраняет journal event и
разрешает повторный approval без `committing` status.

### Test scenario — Supersession связывает две features одним commit

Traces: AC-018

Тест классифицирует изменение committed intent как существенное. Старая feature
сразу становится `superseded`, новая получает изменённый intent и обратную
ссылку, commit затрагивает ровно два feature directories, а отсутствие approval
нового intent не реактивирует старую feature.

## TASK-006 — Реализовать общий author lifecycle в `StageEngine`

Traces: REQ-001, REQ-005, REQ-007, REQ-008, REQ-009, REQ-010, REQ-022, REQ-024, REQ-027, REQ-028, REQ-029, REQ-034, REQ-035, REQ-036, DEC-002, DEC-006, DEC-007, DEC-009, DEC-010

Depends-on: TASK-002, TASK-003, TASK-004

### Outcome

Один data-driven `StageEngine` ведёт author dialogue intent/spec/plan по общей
state machine. Stage policy задаёт роль, upstream documents, fixed artifact,
document contract, доступность review и допустимые команды, не копируя
lifecycle три раза.

### Область и ожидаемые файлы

- `internal/specflow/stage_engine.go`;
- `internal/specflow/stage_policy.go`;
- `internal/specflow/stage_engine_test.go`;
- адаптация existing fake runtime fixtures.

### Запрещённые области

- Git phase commit orchestration;
- reviewer dialogue и findings lifecycle;
- terminal rendering;
- material decisions, принятые engine-ом за пользователя.

### Шаги реализации

1. Определить фиксированные policies intent/spec/plan с author role, artifact
   filename, upstream set, parser mode и review capability.
2. Создавать author thread с read-only project workspace, отдельным artifact
   root и effective prompt из `PromptCatalog`.
3. Обрабатывать `message` как продолжение dialogue без публикации пустого
   artifact placeholder.
4. После каждого artifact сначала резервировать observed IDs, затем запускать
   parser; structural errors возвращать тому же author без review report.
5. Ограничить parser repair тремя автоматическими попытками; после исчерпания
   вернуть concise diagnostics и author dialogue.
6. Первый валидный draft публиковать сразу; последующий обычный draft сохранять
   pending и возвращать diff с revision actions `apply | reject | rework`.
7. На `apply` публиковать pending draft, на `reject` оставлять current revision,
   на `rework` передавать author-у пользовательский scope.
8. Разрешать публикацию явно отложенного вопроса, но сохранять approval blocker
   до явного решения и удаления из `Open questions`.
9. При session close отбрасывать pending artifact через repository и возвращать
   stage к последней published revision.
10. После внешнего изменения основного документа перечитывать его через author
    session и передавать дальнейшую stage-specific реакцию controller-у.

### Task-local технические детали

- Parser attempt counter сбрасывается новым author draft после пользовательского
  вмешательства, но не каждым автоматическим repair turn.
- `StageEngine` возвращает events/effects `FeatureController`; он не изменяет
  `FlowState` самостоятельно.
- Intent policy не предоставляет `/review`; spec и plan policies лишь сообщают
  о capability, а review engine запускается отдельно.
- Diff последующего normal draft является частью revision decision; diff после
  automatic review rework будет informational и обрабатывается TASK-008.

### Test scenario — Первый и последующий drafts имеют разный lifecycle

Traces: AC-003, AC-021

Fake-runtime тест возвращает initial artifact и затем revision. Первый файл
сразу становится published; второй не меняет current document до `apply`.
`reject` сохраняет current bytes, `rework` продолжает тот же author dialogue,
а session close удаляет pending artifact и сохраняет journaled diff.

### Test scenario — Отложенный вопрос публикуется, но не утверждается

Traces: AC-004, AC-023

Author сначала обнаруживает material ambiguity, затем пользователь явно просит
отложить её. Draft с `Open questions` публикуется; попытка approval блокируется.
После пользовательского решения author удаляет вопрос и фиксирует решение без
самостоятельного выбора engine-а.

### Test scenario — Parser repair ограничен тремя автоматическими попытками

Traces: AC-007, AC-010, AC-011

Fake author трижды возвращает структурно неверный artifact. Engine не создаёт
review file, четвёртый automatic turn не запускает и возвращает author dialogue
с diagnostics. Новый draft после пользовательского сообщения начинает новый
counter.

### Test scenario — Одна state machine обслуживает три stage policies

Traces: AC-001, AC-002, AC-021

Table-driven тест прогоняет intent, spec и plan policies через один
`StageEngine`, проверяя отдельные roles/artifact roots, правильные upstream
documents и невозможность использовать review policy на intent.

## TASK-007 — Реализовать запуск review и живой review report

Traces: REQ-012, REQ-013, REQ-014, REQ-015, REQ-016, REQ-017, REQ-018, REQ-019, REQ-022, REQ-039, DEC-006, DEC-007, DEC-008, DEC-009

Depends-on: TASK-002, TASK-003, TASK-004, TASK-006

### Outcome

Review module запускает отдельного spec/plan reviewer-а только по `/review`,
поддерживает один живой numbered report на запуск, выполняет duplicate и
changed-fingerprint protocol и возвращает controller-у либо completed review,
либо material decisions, требующие пользователя.

### Область и ожидаемые файлы

- `internal/specflow/review_engine.go`;
- `internal/specflow/review_run.go`;
- `internal/specflow/review_engine_test.go`;
- review fixtures в `internal/specflow/testdata/`, если они уменьшают дублирование.

### Запрещённые области

- review intent;
- автоматический запуск нового numbered review;
- изменение checked document до material decisions;
- хранение provider session ID или prompt metadata.

### Шаги реализации

1. Разрешить `/review` только для published spec/plan и создать роль
   `spec-reviewer` или `plan-reviewer` с fixed `review.md` artifact.
2. До запуска вычислить target/upstream fingerprint и искать completed review с
   тем же accepted fingerprint.
3. При duplicate вернуть пользователю existing-review result без agent turn и
   нового файла.
4. Перед agent reviewer запускать document parser; structural diagnostics
   возвращать author repair loop без публикации review report.
5. После валидного artifact публиковать `reviews/{stage}-N.md`, добавлять front
   matter и сохранять полный набор findings.
6. Сопоставлять findings с предыдущими reports: сохранять identity неизменённой
   проблемы, создавать новую identity для materially changed problem и
   `superseded` link для прежней.
7. Разделять contract violations с reviewer decision `fix` и material findings
   с `pending/none`.
8. После завершения turn повторно вычислять target/upstream hashes. При
   расхождении вернуть выбор rerun или accept-for-current-revision.
9. При accept сохранить started и accepted fingerprints в state, front matter
   и mem-log; при выборе rerun зафиксировать новый fingerprint и повторить
   reviewer turn в том же numbered run/report без второй команды `/review`.
10. После crash не продолжать reviewer turn автоматически и вернуть stable
    pre-review state.

### Task-local технические детали

- Reviewer читает target, upstream documents, current/previous reports и
  mem-log, но пишет только внешний `review.md`.
- Front matter timestamps/provider/model получает Stepan из runtime metadata;
  reviewer отвечает только за report body.
- Новый review run получает новый retry budget; internal dialogue и rechecks
  обновляют тот же report и тот же counter.
- Stage approval остаётся доступным без review, но после старта блокируется до
  согласованного `completed`.

### Test scenario — Review остаётся optional до первого запуска

Traces: AC-005

Controller fixture утверждает валидные spec и plan без `/review`. Затем другой
flow запускает review: создаётся отдельная reviewer session и numbered report,
а approval блокируется до completed status. Intent policy отвергает `/review`.

### Test scenario — Duplicate fingerprint не запускает agent

Traces: AC-008

После completed review тест повторяет `/review` с теми же target/upstream
hashes. Runtime spy не получает нового thread/turn, review counter и files не
меняются, пользователь получает ссылку на существующий report.

### Test scenario — Changed fingerprint требует явного выбора

Traces: AC-008, AC-023

Во время reviewer turn тест меняет target или upstream document. Review не
становится применимым автоматически: controller запрашивает rerun/accept.
Accept записывает обе пары hashes и делает новый accepted fingerprint
доступным duplicate lookup; rerun повторяет reviewer turn в том же report для
вновь зафиксированного fingerprint.

### Test scenario — Повторный report сохраняет findings и immutable fields

Traces: AC-009

Тест начинает новый review после изменения fingerprint. Report перечисляет
все прежние findings с текущими statuses; неизменённая проблема сохраняет ID и
severity, materially changed problem создаёт новый ID и supersedes прежний.

## TASK-008 — Реализовать material decisions и automatic rework/recheck

Traces: REQ-005, REQ-018, REQ-019, REQ-020, REQ-021, REQ-022, REQ-024, DEC-007, DEC-008, DEC-009

Depends-on: TASK-006, TASK-007

### Outcome

Review lifecycle обсуждает с пользователем только material findings. После
разрешения последнего вопроса Stepan автоматически передаёт current review
author-у, публикует scoped rework и тем же reviewer-ом подтверждает исправления
с общим лимитом три попытки.

### Область и ожидаемые файлы

- `internal/specflow/review_engine.go` — decision/rework state machine;
- `internal/specflow/stage_engine.go` — режим review-scoped author rework;
- `internal/specflow/review_rework_test.go`.

### Запрещённые области

- передача author-у свободной reviewer conversation вместо review-файла;
- revision decision для automatic rework diff;
- dismissal contract violation;
- сброс retry counter при новом finding внутри того же run.

### Шаги реализации

1. Представлять pending material findings как ordered decision queue и
   продолжать reviewer dialogue свободным текстом.
2. Обработать `/apply` как user decision `fix` для всех pending material
   findings; свободным текстом разрешать отдельные fix/dismiss decisions.
3. Требовать user rationale для dismissal и писать provenance в current report.
4. После последнего material decision явно создать progress event о запуске
   rework и автоматически вызвать текущую author session.
5. Передать author-у только current review report и ограничение согласованного
   scope; автоматически опубликовать валидный artifact.
6. Показать diff как informational progress, не создавая pending revision
   decision.
7. Запустить того же reviewer-а для recheck, обновить statuses/resolutions в том
   же report и проверить фактическое исправление каждого contract/fix finding.
8. Если author или reviewer обнаружил новое material decision, остановить loop,
   опубликовать finding и вернуть пользователя в reviewer dialogue.
9. Сохранять retry counter при новом finding; не запускать четвёртую automatic
   attempt и переводить flow в escalated author dialogue с concise report.
10. После escalation разрешить dialogue с author, но новый review budget выдавать
    только следующей явной `/review`.

### Task-local технические детали

- Contract findings получают reviewer provenance при первом report и всегда
  входят в rework scope.
- Retry counter относится ко всему reviewer → author → reviewer run, а не к
  отдельному finding.
- Recheck может переводить finding в `resolved`, оставлять `open` или добавить
  новый finding; исходные immutable поля не переписываются.
- Author-generated ID резервируется до parser validation и не освобождается при
  неуспешном rework.

### Test scenario — Contract и material findings проходят разный путь

Traces: AC-006, AC-023

Review report содержит contract violation и два material findings. Contract
finding автоматически получает reviewer/fix; до решений пользователя author
не вызывается. `/apply` принимает оставшиеся рекомендации, запускает rework
один раз и сохраняет user provenance.

### Test scenario — Dismissal material finding не меняет документ

Traces: AC-009, AC-023

Пользователь отклоняет finding с rationale. Report получает `dismissed`,
`Decided-by: user` и rationale; finding не передаётся author-у как изменение.
Попытка тем же способом отклонить contract violation возвращает blocking error.

### Test scenario — Rework публикуется автоматически и подтверждается reviewer-ом

Traces: AC-006

После последнего material decision fake author возвращает scoped artifact.
Repository публикует его без revision prompt, Progress содержит informational
diff, тот же reviewer rechecks report и переводит исправленные findings в
`resolved` с `Resolution`.

### Test scenario — Retry exhaustion и новый finding сохраняют общий counter

Traces: AC-007

Reviewer дважды оставляет проблему open, затем создаёт новое material finding.
После user decision loop использует оставшийся budget; четвёртый automatic turn
не запускается, status становится `escalated`, а новый `/review` создаёт новый
run с полным budget.

## TASK-009 — Собрать canonical flow в `FeatureController`

Traces: REQ-001, REQ-002, REQ-003, REQ-004, REQ-005, REQ-007, REQ-009, REQ-010, REQ-012, REQ-037, REQ-038, REQ-045, REQ-047, REQ-049, DEC-001, DEC-002, DEC-004, DEC-006, DEC-012

Depends-on: TASK-005, TASK-006, TASK-007, TASK-008

### Outcome

`FeatureController` становится единственным state owner и связывает
`StageEngine`, review lifecycle и `FeatureRepository` в фиксированный порядок
intent → spec → plan. Все команды проверяются относительно одного snapshot
state и возвращают `Progress` без копирования правил в callers.

### Область и ожидаемые файлы

- `internal/specflow/controller.go`;
- `internal/specflow/controller_test.go`;
- перенос применимых scenarios из `intent_flow_test.go`.

### Запрещённые области

- прямые filesystem/Git writes из controller;
- terminal input/output;
- provider-specific branches;
- возврат от spec к intent как обычный transition.

### Шаги реализации

1. Определить controller commands/events для author messages, artifact turns,
   revision decisions, `/review`, review decisions, `/approve`,
   `/revise-spec`, `/status` и session close.
2. На каждой команде загрузить repository snapshot, обнаружить external changes
   и проверить допустимость перехода.
3. Разрешить spec только при committed intent и plan только при committed spec.
4. Для `/approve` запросить document/review/open-question validation и одну
   repository phase-approval operation; следующую stage открыть только после
   успешного commit result.
5. Не требовать agent review до его запуска; после запуска учитывать review
   status как независимый approval blocker.
6. Сформировать context-specific `Progress`, включающий current stage/status,
   review status, document paths, diagnostics и ordered command hints.
7. После committed plan оставить flow `active`, current stage `plan`, stage
   `committed`; не объявлять flow завершённым.
8. Сохранить все user/material decisions в mem-log через repository operation
   до соответствующего state transition.
9. Гарантировать, что error result не оставляет in-memory state, расходящийся с
   durable snapshot.

### Task-local технические детали

- Controller вызывает глубокие repository operations и не знает порядок записи
  `state.json`, `mem-log.md`, документов и Git index.
- Stage/review engines вычисляют эффекты, но только controller принимает новый
  `FlowState` после успешного durable result.
- `/approve` без запущенного review допустим; `/approve` при running,
  awaiting_decisions, automatic_rework или escalated review недопустим.
- Неизвестная или контекстно недоступная команда возвращает текущее состояние и
  допустимые hints без мутации.

### Test scenario — Полный flow соблюдает stage gates

Traces: AC-001, AC-002, AC-021

Тест через `FeatureController` пытается начать spec/plan преждевременно и
получает invalid transition. После каждого успешного approval/commit открывается
ровно следующая author role; committed plan оставляет active flow на plan.

### Test scenario — Optional и начатый review по-разному влияют на approval

Traces: AC-005, AC-023

Один flow утверждает spec без review. Во втором после `/review` controller
блокирует approval на каждом незавершённом review status и разрешает его только
после contract fixes и user decisions.

### Test scenario — Approval preflight не создаёт частичный transition

Traces: AC-004, AC-016, AC-020, AC-021

Table-driven test подставляет open question, protected-artifact modification,
dirty outside path и commit failure. В каждом случае controller возвращает все
reasons, не открывает следующую stage и при повторной команде начинает с
durable state repository.

## TASK-010 — Реализовать stage-specific external revisions

Traces: REQ-003, REQ-004, REQ-010, REQ-011, REQ-037, REQ-038, REQ-045, REQ-046, REQ-047, DEC-001, DEC-004, DEC-006, DEC-012

Depends-on: TASK-009

### Outcome

`FeatureController` обрабатывает допустимые внешние изменения intent/spec/plan
по правилам их стадии: intent classification, `/revise-spec`, plan invalidation
и защита service-owned artifacts.

### Область и ожидаемые файлы

- `internal/specflow/controller.go` — external revision transitions;
- `internal/specflow/repository.go` — domain operations revision/supersession;
- `internal/specflow/controller_revision_test.go`.

### Запрещённые области

- обычный transition от spec к intent;
- снятие downstream approvals при несущественном intent change;
- снятие spec approval до фактического изменения bytes;
- автоматическая классификация существенности intent без пользователя.

### Шаги реализации

1. После изменения основного документа запустить соответствующую author session
   для перечитывания, parser validation и фиксации issued IDs.
2. Для committed intent запросить у пользователя material classification.
3. Несущественное изменение intent принять как новую approved revision,
   сохранить downstream approvals, заменить approved hash и немедленно вызвать
   `revise intent` repository operation.
4. Существенное изменение передать атомарной supersession operation из TASK-005
   и ждать approval скопированного intent в новой feature.
5. На `/revise-spec` открыть/reuse spec author dialogue, оставив approval до
   фактического изменения `spec.md`.
6. Если spec bytes не изменились, `/approve` вернуть flow к plan без commit.
7. После фактического изменения снять spec approval; после нового
   approval/commit вернуть plan с сохранённым историческим approval и
   `outdated: true`.
8. После внешнего изменения plan снять текущий approval и вернуть plan в
   published lifecycle.
9. Любое изменение service-owned artifact оставить blocking condition до
   восстановления согласованного состояния.

### Task-local технические детали

- Сравнение фактической revision выполняется по document hash, а не timestamp.
- Живой author thread переиспользуется; при его отсутствии восстановление будет
  выполнять session registry из TASK-011.
- `outdated` устанавливается сразу после reapproval изменённой spec.
- Intent supersession сохраняет исходную feature закрытой даже при отказе от
  дальнейшего approval новой feature.

### Test scenario — `/revise-spec` различает unchanged и changed spec

Traces: AC-017

Тест входит в plan, вызывает `/revise-spec` и сначала не меняет document.
`/approve` возвращает plan без commit. Во второй ветке spec меняется: approval
снимается только после новых bytes, требуется новый spec commit, а plan
возвращается с `outdated: true` и требует повторного approval.

### Test scenario — Intent revision требует user classification

Traces: AC-018, AC-023

После внешнего изменения committed intent controller не выбирает ветку сам.
Ответ «несущественное» сохраняет downstream approvals, обновляет hash и создаёт
revision commit; ответ «существенное» выполняет linked supersession и переводит
пользователя к approval intent новой feature.

### Test scenario — External plan revision снимает только plan approval

Traces: AC-020

Тест меняет committed `plan.md` через разрешённый внешний путь. Controller
перечитывает его author-ом, валидирует и переводит plan в published, не меняя
committed intent/spec. Аналогичное изменение review/state/journal блокирует
flow вместо запуска author-а.

## TASK-011 — Добавить session registry, exit и resume recovery

Traces: REQ-002, REQ-008, REQ-013, REQ-037, REQ-038, REQ-040, REQ-041, REQ-042, REQ-043, DEC-001, DEC-004, DEC-007

Depends-on: TASK-009, TASK-010

### Outcome

Process-level session registry переиспользует живые author/reviewer threads по
feature и role, а новый процесс восстанавливает dialogue из authoritative
documents, reports и mem-log. `/exit`, EOF, Ctrl+C и crash возвращают flow к
последнему устойчивому состоянию без отмены feature.

### Область и ожидаемые файлы

- `internal/specflow/session_registry.go`;
- `internal/specflow/session.go`;
- `internal/specflow/resume.go`;
- `internal/specflow/session_test.go` и `resume_test.go`.

### Запрещённые области

- сохранение provider thread handle в `state.json`;
- автоматическое продолжение interrupted review/rework turn;
- запись `flow canceled`;
- отдельная команда отмены flow.

### Шаги реализации

1. Индексировать живые sessions по feature ID и role, поддерживая отдельные
   author/reviewer threads.
2. При запросе роли переиспользовать доступный thread; иначе создать новый из
   role prompt, current/upstream documents, current/previous reviews и typed
   mem-log context.
3. На `/exit`, EOF и Ctrl+C закрыть все sessions данной feature, удалить
   artifact roots и отбросить pending draft через repository.
4. Не менять `flow_status`; сохранить published/committed state и запись о
   session close/recovery в журнале.
5. При resume `running` review или `automatic_rework` восстановить последний
   устойчивый state и потребовать новый явный `/review`.
6. Отделить discovery resumable flows от activation выбранной feature.
7. Блокировать activation другой feature, если repository обнаруживает
   uncommitted changes у активной feature.
8. Исключить superseded features и active flows с committed plan из resumable
   result до появления implementation stage.

### Task-local технические детали

- Durable context содержит только artifacts и журнал; runtime-specific handle
  живёт в process memory registry.
- Reviewer recovery читает current и previous numbered reports, но пишет новый
  report только после нового `/review`.
- Session close идемпотентен, чтобы EOF/Ctrl+C после `/exit` не создавали
  дополнительные transitions.
- Recovery event не трактуется как material decision пользователя.

### Test scenario — Живые threads переиспользуются раздельно по ролям

Traces: AC-001, AC-005, AC-017, AC-022

Runtime spy проходит intent, spec review, plan и возврат к spec. В пределах
процесса каждая role создаёт один thread и получает последующие turns в него;
author и reviewer никогда не разделяют thread.

### Test scenario — Новый процесс восстанавливает flow без thread handle

Traces: AC-014, AC-022

Тест закрывает registry после published spec с review history и запускает новый
instance. Resume создаёт новый spec role thread из документов/report/mem-log,
продолжает stable state и не находит provider identifier в `state.json`.

### Test scenario — Exit во время pending revision не отменяет feature

Traces: AC-003, AC-014

Три table cases вызывают `/exit`, EOF и Ctrl+C при pending revision. Artifact
root удалён, published revision сохранена, flow остаётся active, pending diff
остается в журнале, а повторный resume начинает с published state.

### Test scenario — Interrupted review требует нового `/review`

Traces: AC-007, AC-014

Fault-injection останавливает процесс при `running` review и
`automatic_rework`. Новый process восстанавливает предыдущее устойчивое
состояние, не вызывает runtime автоматически и показывает `/review` как
необходимое явное действие.

## TASK-012 — Перевести terminal UI на `Progress` и контекстные команды

Traces: REQ-041, REQ-042, REQ-048, REQ-049, DEC-003

Depends-on: TASK-009, TASK-011

### Outcome

Terminal UI отображает состояние, diagnostics, diff и только команды из
`Progress`. Main prompt поддерживает `/feature` и `/resume`, flow prompt —
контекстные команды и revision decisions, а `/resume` выбирает flow номером.

### Область и ожидаемые файлы

- `internal/specflow/ui_model.go`;
- `internal/specflow/ui.go`;
- `internal/specflow/interactive.go`;
- `internal/specflow/ui_test.go` и interactive tests.

### Запрещённые области

- вычисление допустимых transitions внутри UI;
- hard-coded role/capability composition;
- скрытые команды, отсутствующие в `Progress`;
- показ committed-plan flows или superseded flows в `/resume`.

### Шаги реализации

1. Отобразить каждый `CommandHint` таблицей `Command | Description` перед
   пользовательским prompt.
2. Отдельной строкой сообщать, разрешён ли ordinary text в текущем контексте.
3. Отобразить revision actions `apply | reject | rework` отдельной таблицей,
   когда controller ожидает revision decision.
4. Поддержать `/review`, `/apply`, `/approve`, `/revise-spec`, `/status` и
   `/exit` только когда они присутствуют в `Progress`.
5. На main prompt показывать `/feature` и `/resume`.
6. Рендерить `/resume` как `№ | Feature | Current stage | Stage status |
   Review status | Updated` и принимать только номер существующей строки.
7. Передавать ordinary text и commands controller-у без локального изменения
   state.
8. Связать `/exit`, EOF и Ctrl+C с одним idempotent session-close path.

### Task-local технические детали

- Порядок строк command table равен порядку hints controller-а.
- Пустой список resumable flows рендерится как информационное состояние без
  фиктивной строки выбора.
- Diagnostics от approval/recovery отображаются до следующей command table.
- UI не интерпретирует review status и не выводит команды по собственным
  условиям.

### Test scenario — Каждый prompt показывает только допустимые команды

Traces: AC-019

Snapshot/table-driven UI tests подают `Progress` для drafting, pending revision,
published spec, awaiting review decisions, escalated review и committed plan.
В output присутствуют ровно переданные команды с descriptions и корректная
строка ordinary-text availability.

### Test scenario — Resume table фильтруется и выбирается номером

Traces: AC-014, AC-015, AC-019

UI model получает active resumable, superseded и committed-plan flows.
Отображаются только допустимые строки с шестью согласованными columns; valid
number активирует нужную feature, invalid number не меняет controller state.

### Test scenario — Три способа выхода используют один lifecycle

Traces: AC-003, AC-014

Interactive tests подают `/exit`, EOF и Ctrl+C и ожидают одинаковый вызов
session close, отсутствие cancel event и возврат в main prompt с сохранённой
feature.

## TASK-013 — Обеспечить parity Codex и Claude adapters

Traces: REQ-002, REQ-013, REQ-030, REQ-031, REQ-036, REQ-042, DEC-005, DEC-007, DEC-010, DEC-011

Depends-on: TASK-001, TASK-002, TASK-011

### Outcome

Codex и Claude adapters реализуют один author/reviewer/session/artifact
contract. Codex использует flat response schema, Claude локально нормализует
свой transport к тому же доменному envelope, а оба поддерживают независимые
threads и read-only workspace.

### Область и ожидаемые файлы

- `internal/codexapp/thread.go` и его tests;
- `internal/claudeapp/*` и его tests;
- `internal/agentruntime/conformance_test.go` либо существующий общий
  conformance harness;
- минимальные изменения `internal/agentruntime` только при необходимости общего
  immutable contract.

### Запрещённые области

- provider-specific ветвления в `FeatureController`/`StageEngine`;
- union keywords в Codex `text.format.schema`;
- optional transport properties, отсутствующие в `required`;
- writable project workspace или artifact path в agent response.

### Шаги реализации

1. Обновить Codex response schema до flat object со всеми keys в `required` и
   `kind`, допускающим `message | artifact`.
2. Передавать для artifact обязательный `message: ""` и нормализовать его до
   domain envelope в `specflow`.
3. Адаптировать Claude immutable schema/decoder к тем же двум domain outcomes.
4. Для обеих реализаций передавать effective role prompt, read-only workspace и
   отдельный writable artifact root с fixed filename.
5. Поддержать несколько одновременных threads на session и независимое закрытие
   author/reviewer roles.
6. Расширить provider-neutral conformance harness author, reviewer, resume,
   artifact output, message output и close semantics.

### Task-local технические детали

- Domain normalization остаётся в `specflow`; adapters отвечают за transport
  decode и metadata provider/model.
- Bootstrap/effective instructions передаются один раз при создании thread;
  subsequent turns продолжают тот же thread без повторной composition.
- Artifact bytes читаются Stepan-ом только из ожидаемого файла текущего
  artifact root.
- Provider metadata разрешено только в review front matter и runtime events, не
  в durable state.

### Test scenario — Flat Codex schema соответствует transport ограничениям

Traces: AC-013, AC-022

Schema test ожидает flat object без `oneOf`, полный `required` для всех
properties, два допустимых `kind` и отсутствие artifact path. Decoder принимает
пустой artifact message и отклоняет missing/extra incompatible fields.

### Test scenario — Один conformance suite проходит для двух adapters

Traces: AC-022

Общий suite запускает author message/artifact, reviewer artifact, несколько
role threads, close и новую resume session для Codex и Claude fixtures.
Наблюдаемые domain events, workspace permissions и artifact filenames
совпадают.

### Test scenario — Provider не может писать project artifacts напрямую

Traces: AC-020, AC-022

Conformance fixture пытается вернуть path и записать вне artifact root.
Adapter/domain contract отклоняет path, project workspace остаётся read-only,
а валидный artifact публикуется только последующей repository operation.

## TASK-014 — Подключить полный flow и удалить intent-only lifecycle

Traces: REQ-001, REQ-002, REQ-006, REQ-012, REQ-026, REQ-030, REQ-037, REQ-041, REQ-042, REQ-045, REQ-048, REQ-049, DEC-001, DEC-002, DEC-003, DEC-004, DEC-005, DEC-007, DEC-010

Depends-on: TASK-005, TASK-006, TASK-007, TASK-008, TASK-009, TASK-010, TASK-011, TASK-012, TASK-013

### Outcome

`cmd/stepan` запускает новый durable planning flow end-to-end. Старый
intent-only controller contract, состояния, targets и bootstrap path удалены;
единственным supported lifecycle становится intent → spec → plan с optional
review и resume.

### Область и ожидаемые файлы

- `cmd/stepan/main.go` и composition-root tests;
- оставшиеся `internal/specflow/*.go` и tests, использующие старые contracts;
- удаление или переписывание `intent_flow_test.go` после переноса scenarios;
- `arch-go.yml`, только если новые imports требуют отражения уже согласованных
  module interfaces.

### Запрещённые области

- compatibility layer, сохраняющий старый плоский state как второй lifecycle;
- documentation/execution stages;
- project-specific deterministic check configuration;
- новые provider-specific rules в composition root.

### Шаги реализации

1. Собрать `PromptCatalog`, `FeatureRepository`, `StageEngine`, session registry,
   runtime adapter и `FeatureController` в composition root.
2. Переключить main loop между main prompt, new feature и numbered resume.
3. Передавать UI только `Progress` и user input; lifecycle effects оставлять в
   controller/repository.
4. Удалить прежние intent-only states, cancel event, один общий thread/artifact
   root и устаревший `specification.md` target.
5. Удалить старый bootstrap prompt после того, как все роли используют
   `PromptCatalog`.
6. Перенести ценные intent scenarios на interfaces нового flow и удалить tests,
   фиксирующие заменённые internal states.
7. Добавить end-to-end fixtures полного author/review/rework/approval/resume
   lifecycle через fake runtime и temporary Git repository.
8. Оставить committed plan как active hidden flow и не запускать отсутствующую
   implementation stage.

### Task-local технические детали

- Composition root выбирает runtime adapter, но не prompt composition и не
  stage policy.
- Existing `agentruntime.Runtime` остаётся provider-neutral seam; расширять его
  следует только при невозможности выразить согласованный thread lifecycle.
- Architecture dependency rules должны сохранять `specflow` владельцем
  planning domain, а provider adapters — внешними реализациями runtime seam.

### Test scenario — End-to-end flow проходит три стадии и два reviews

Traces: AC-001, AC-002, AC-005, AC-006, AC-016, AC-019, AC-021, AC-023

Integration test через composition root создаёт feature, утверждает intent,
создаёт/reviews/исправляет/утверждает spec, создаёт/reviews/исправляет/утверждает
plan. Он наблюдает правильные command hints, отдельные roles, numbered reports,
feature-only commits и active hidden state после committed plan.

### Test scenario — End-to-end resume продолжает stable published revision

Traces: AC-003, AC-007, AC-014, AC-015, AC-022

Test process останавливается с pending author revision и отдельно во время
review rework, затем создаёт новый composition root. Resume list фильтруется,
новые provider sessions получают durable context, pending bytes отсутствуют,
а interrupted review ждёт явного `/review`.

### Test scenario — End-to-end revision paths сохраняют upstream consistency

Traces: AC-008, AC-017, AC-018, AC-020

Integration test покрывает changed fingerprint, unchanged/changed
`/revise-spec`, несущественную intent revision и существенную supersession.
Hashes, links, outdated flag, approvals и protected-artifact blockers остаются
согласованными после повторного открытия repository.

## Open questions
