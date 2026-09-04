---
review_id: PLAN-REVIEW-001
stage: plan
status: awaiting_decisions
created_at: 2026-09-04T08:21:35.845293+03:00
updated_at: 2026-09-04T08:25:21.6971954+03:00
provider: "codex"
model: "default"
started_revision: a8e5e6b3d1c9526341fb1326919128260f7855d040a0a803ad2a349c5187a2b0
accepted_for_revision: a8e5e6b3d1c9526341fb1326919128260f7855d040a0a803ad2a349c5187a2b0
upstream_started:
  intent.md: c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c
  spec.md: 65134090cfb0d8d6321360b6207259ef9023081dae8088d7468e1da711c67327
upstream_accepted:
  intent.md: c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c
  spec.md: 65134090cfb0d8d6321360b6207259ef9023081dae8088d7468e1da711c67327
attempts: 0
---

# Ревью плана реализации

## PLAN-F-001 — Неоднозначно ограничено число допустимых записей за один turn

Severity: major
Status: open
Problem: В `TASK-003` outcome обещает разрешать «ровно одну» корректно коррелированную write/edit operation, а test scenario ожидает, что разрешена «только первая» операция текущего turn. Это можно реализовать как лимит в одну файловую операцию на весь turn, хотя `REQ-013` и Decision D-013 требуют одноразового решения для каждого ACP permission request и не запрещают несколько различных корректных write/edit requests в одном turn. Такой лимит способен сорвать обычное создание и последующее исправление одного артефакта в рамках хода и противоречит полному flow parity.
Location: TASK-003 Outcome; TASK-003 шаг 4; Test scenario — Write approval связан с активным turn и artifact root
Traces: TASK-003
Recommendation: Явно определить cardinality на уровне permission request: разрешать любое число различных корректно коррелированных write/edit requests активного turn, выдавая каждому отдельное одноразовое решение, и отклонять только duplicate/stale/foreign либо иначе невалидные requests. Добавить в scenario как минимум две разные допустимые операции одного turn наряду с duplicate request.
Decision: pending
Decided-by: none
Rationale:

## PLAN-F-002 — Сторонний CLI не проходит требуемый полный flow

Severity: major
Status: open
Problem: `AC-014` требует, чтобы executable с нестандартным basename прошёл тот же Qwen-compatible contract и полный `/feature` flow. `TASK-006` проверяет для такого файла лишь выбор executable, `TASK-007` прогоняет полный flow только для обобщённого fake Qwen adapter, а `TASK-008` ограничивает real-CLI scenario ACP/tool/file-policy probes и упоминает лишь «минимальный resumable path». Ни один test scenario не связывает нестандартно названный сторонний executable с полным author/reviewer dialogue, decisions, rework, approval, close и resume, поэтому ключевое обещание vendor-neutral parity остаётся непроверенным.
Location: TASK-006 Test scenario — CLI выбирает только запрошенный provider; TASK-007 Test scenario — Один shared flow проходит с тремя adapters; TASK-008 шаг 9 и Test scenario — Реальный совместимый CLI проходит изолированный contract
Traces: TASK-006, TASK-007, TASK-008
Recommendation: Расширить shared full-flow scenario или opt-in real-CLI scenario так, чтобы exact explicit executable с нестандартным basename проходил полный набор стадий и переходов из `AC-003`/`AC-014`, включая reviewer, material decision, automatic rework, approval, close и fresh resume, без branding/version probe или fallback.
Decision: pending
Decided-by: none
Rationale:

## PLAN-F-003 — Safe diagnostics покрыты не для всех обязательных категорий ошибок

Severity: major
Status: open
Problem: `AC-015` требует fixtures каждой категории из `REQ-019`, но план проверяет sanitization только для части startup/containment и protocol failures. Сценарии permission denial, repair exhaustion и operator cancellation проверяют функциональный исход, однако не требуют различимой provider-neutral classification и отсутствия credentials, environment secrets, полных prompt/response bodies и запрещённого file content; executable и capability failures также не собраны в полную diagnostic matrix. Фраза `TASK-008` о safe diagnostic failures не задаёт эти setup/action/expected assertions и не трассируется к отдельному сценарию `AC-015`.
Location: TASK-001 Test scenario — Startup failure не оставляет process tree и секреты; TASK-003 Test scenario — Write approval связан с активным turn и artifact root; TASK-004 Test scenario — Repair имеет ровно три response attempts; TASK-005 Test scenario — Interrupt соблюдает cancel grace и закрывает всё; TASK-008 шаг 9
Traces: TASK-001, TASK-003, TASK-004, TASK-005, TASK-008
Recommendation: Добавить автоматизированный diagnostic-matrix scenario с отдельной fixture для missing/non-regular executable, handshake/capability incompatibility, process exit/protocol violation, permission denial, repair exhaustion и cancellation. Для каждой ветки зафиксировать ожидаемый provider-neutral cause, допустимый Qwen context и отрицательные assertions на все запрещённые данные.
Decision: pending
Decided-by: none
Rationale:

## PLAN-F-004 — Первый session prompt и immutable schema не имеют прямого тестового сценария

Severity: minor
Status: open
Problem: Шаги `TASK-004` правильно требуют process-level JSON contract, role bootstrap, exact schema и session context в первом prompt, отсутствие повторного bootstrap в следующих ordinary prompts и immutable schema на lifetime thread, но оба task-local test scenarios начинаются уже с ответов fake agent и проверяют только assembler/repair. Они не наблюдают отправленные prompts и не способны обнаружить пропуск role/session context, повтор bootstrap либо подмену schema после `StartThread`, поэтому `REQ-008` покрыт декларативно, но не объективно тестируется.
Location: TASK-004 шаги 1–2; Test scenario — Fragmented JSON становится одним валидным envelope; Test scenario — Repair имеет ровно три response attempts
Traces: TASK-004
Recommendation: Добавить scenario с записывающим fake ACP agent: проверить общий process-level contract, точное наличие role instructions, schema и session context только в первом session prompt, точную schema в repair prompt и неизменность cloned schema после мутации исходного caller buffer; последующий ordinary prompt не должен повторять bootstrap.
Decision: pending
Decided-by: none
Rationale:
