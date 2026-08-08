# Lifecycle, контекст и восстановление

## Модель состояний

Авторитетный task lifecycle:

```text
planned → ready → in_progress → merge_ready → done
                         ├───────────────→ blocked
                         └───────────────→ failed
```

Состояние конкретной попытки:

```text
selection → briefing → implementation/testing → gates → verification
                                           verification → rework → new candidate
                                           verification → merge_ready → developer merge → closure
                                           any safe boundary → parked/escalation
```

Parking является состоянием исполнения, а не успешным или неуспешным завершением задачи.

## Lifecycle, scheduler и бюджеты

**LIFE-001 [P0].** Task lifecycle MUST быть единственной бизнес-истиной статуса задачи. Checkpoint MUST NOT становиться вторым task registry.

**LIFE-002 [P0].** Только Controller MUST применять переходы lifecycle. Роли могут возвращать предложения, факты и verdicts.

**LIFE-003 [P0].** Controller MUST проверять preconditions, ожидаемую предыдущую версию и идемпотентность каждого перехода.

**LIFE-004 [P0].** Перед исполнением Controller MUST выдавать задаче lease с владельцем, сроком, attempt ID и base SHA.

**LIFE-005 [P0].** Задача может перейти в `ready` только после выполнения зависимостей и admission checklist.

**LIFE-006 [P0].** Каждый run MUST иметь настраиваемые hard limits времени, dispatches, attempts и, если провайдер предоставляет данные, tokens/cost.

**LIFE-007 [P0].** При достижении hard limit Controller MUST прекратить новые действия и создать `BUDGET_EXCEEDED` с terminal route.

**LIFE-008 [P1].** Retry и rework budgets MUST учитываться раздельно. Rework по умолчанию ограничивается тремя полными циклами, но число является policy, а не константой продукта.

**LIFE-009 [P1].** Повтор MUST быть разрешён только при изменённом brief, конкретном finding, исправленной среде, решении владельца, одобренном новом routing или классифицированном transient failure.

**LIFE-010 [P1].** Parking record MUST содержать task/attempt ID, candidate SHA, stage, last durable facts, blocker code, owner, safe next action и unsafe-to-repeat actions.

**LIFE-011 [P1].** Controller MUST продолжать независимые готовые задачи при парковке одной ветви, если безопасность и dependencies это допускают.

**LIFE-012 [P3].** Parallel scheduler MUST строить waves только из задач с выполненными зависимостями, совместимыми write sets и отсутствующими конфликтами shared/frozen зон.

**LIFE-013 [P0].** Core task status enum MUST быть закрытым: `planned`, `ready`, `in_progress`, `merge_ready`, `done`, `blocked`, `failed`. Workflow MAY определять внутренние attempt stages, но MUST NOT добавлять, переименовывать или переопределять смысл core statuses.

## Контекст и память

**CTX-001 [P1].** Stepan MUST разделять:

- durable product truth;
- durable execution state;
- ephemeral compiled context;
- observation journal.

**CTX-002 [P1].** Context compiler MUST собирать минимально достаточный контекст из governing requirements, прямых ADR/контрактов, scoped files, релевантных свежих evidence и явных unknowns.

**CTX-003 [P1].** Role profile MUST задавать soft и hard context budgets. При приближении к hard limit Controller MUST checkpoint/park/reset, а не полагаться на непрозрачную compaction.

**CTX-004 [P1].** После compaction или потери контекста результаты незавершённой модельной сессии MUST считаться недоверенными, пока их не подтверждают durable facts.

**CTX-005 [P1].** Handoff MUST иметь схему: task, attempt, candidate SHA, выполненные этапы, observed facts, pending work, blockers, unsafe actions и next safe action.

**CTX-006 [P1].** При возобновлении Controller MUST сверять handoff с local Git и фактами явно подключённых adapters; при расхождении побеждает более авторитетный источник.

**CTX-007 [P1].** Explorer MUST работать read-only в отдельном контексте и возвращать path/line, найденный контракт, релевантность, confidence и unknowns.

**CTX-008 [P1].** Explorer report MUST быть привязан к ревизии и инвалидироваться при изменении затронутых файлов.

**CTX-009 [P1].** Повторяющиеся обращения к Explorer MUST быть измеримы и давать предупреждение о слабом brief или цикле.

## Восстановление

**REC-001 [P1].** После kill/restart Stepan MUST продолжить без истории чата, используя durable records.

**REC-002 [P1].** Recovery-first порядок MUST быть: загрузить trusted policy; сверить local Git/worktree и факты подключённых adapters; найти незакрытые leases; сопоставить checkpoint/handoff/verdict; восстановить или заблокировать незавершённое; затем выбирать новую задачу.

**REC-003 [P1].** До выполнения side effect его тип MUST быть классифицирован как read-only, idempotent, local-reversible или external/non-idempotent.

**REC-004 [P1].** External/non-idempotent product action MUST быть запрещено. Вызов agent adapter может передавать данные разрешённому provider-у, но MUST NOT предоставлять агенту capability для мутации внешней продуктовой системы.

## Отказы и эскалация

**FAIL-001 [P0].** Минимальный обязательный словарь: `SPEC_GAP`, `CONTEXT_INSUFFICIENT`, `IMPLEMENTATION_FAILURE`, `TEST_FAILURE`, `BOUNDARY_VIOLATION`, `BUDGET_EXCEEDED`, `UNSUPPORTED_RISK_PROFILE`, `SANDBOX_UNAVAILABLE`.

**FAIL-002 [P1].** Расширяемый словарь SHOULD включать `BASELINE_FAILURE`, `ENVIRONMENT_FAILURE`, `SECURITY_HALT` и `OWNER_DECISION_REQUIRED`, когда для них существуют отличающиеся recovery routes.

**FAIL-003 [P0].** Failure record MUST содержать code, detail, evidence, owner, retry safety, next safe action и terminal route.

**FAIL-004 [P0].** `BOUNDARY_VIOLATION` и `SECURITY_HALT` MUST немедленно прекращать полномочия затронутой роли.

**FAIL-005 [P1].** Структурированное observation MUST позволять записать проблему вне scope без её автоматического исправления: kind, location, evidence, impact, urgency и `not_part_of_current_task=true`.

**FAIL-006 [P1].** Stepan MUST объединять запросы Owner в краткие decision cards: вопрос, варианты, последствия, рекомендация, безопасный default и затронутые задачи.

## Learning loop

**LEARN-001 [P3].** После wave/stage система SHOULD агрегировать failures, hook violations, rework reasons, escaped defects, context resets, cost/time и owner decisions.

**LEARN-002 [P3].** Изменение policy, prompt, hook, routing или workflow SHOULD ссылаться на наблюдавшийся failure, regression fixture или измеримую цель.

**LEARN-003 [P3].** Инцидент SHOULD создавать или обновлять regression/eval fixture, если класс отказа воспроизводим.
