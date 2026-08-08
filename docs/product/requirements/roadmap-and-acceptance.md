# Этапы поставки и критерии приёмки

## P0. Safe walking slice

Состав:

- открытый Agent Adapter Contract и минимум один reference adapter;
- один последовательный lane;
- plan generation/review, task admission и стабильный lifecycle;
- trusted policy, role profiles, budgets и hard halt;
- отдельный worktree, обязательный sandbox и enforced write boundary;
- Implementation Worker, deterministic gates и read-only Verifier;
- baseline provenance, evidence store и spec/SHA-bound verdict;
- локальный candidate commit, `merge_ready` handoff разработчику и same-SHA check после merge;
- `start/run/status/review` и явный halt.

P0 принят, если автоматизированные end-to-end сценарии доказывают:

1. Worker не может записать файл вне boundary или изменить tests.
2. Verifier не может изменить candidate.
3. Candidate не может переписать trusted review policy.
4. Изменение SHA или spec revision инвалидирует approval.
5. Задача не закрывается без evidence для всех обязательных AC.
6. Исчерпание budget создаёт явный halt, а не скрытое продолжение.
7. Existing baseline failure не приписывается кандидату и не маскируется.
8. Stepan не выполняет merge; после merge разработчиком closure отклоняется, если target branch не указывает на approved SHA.

## P1. Context and recovery

Состав:

- task/brief compiler и gap protocol;
- Explorer и context budgets;
- checkpoints, handoffs, parking и recovery-first scheduler;
- bounded rework и safe-replay classification;
- capability broker для local Git mutations и provider credentials;
- audit trail, observation registry и decision cards.

P1 принят, если:

1. Kill/restart на каждом этапе восстанавливает run без чата и без дублирования side effects.
2. `CONTEXT_INSUFFICIENT` приводит к rebrief, а `SPEC_GAP` — к изменению governing source/решению Owner.
3. Parking сохраняет кандидата и точный safe resume route.
4. Повтор без новой информации блокируется после policy budget.
5. Независимые задачи могут продолжаться при локальном blocker-е.

## P2. Independence and risk

Состав:

- независимый Test Worker;
- risk classifier;
- adversarial verifier для `critical` задач;
- scoped credentials и дополнительные high-risk sandbox profiles;
- trusted-ref review и test-quality gates;
- property/contract/mutation integrations в project profiles.

P2 принят, если:

1. Code и test workers физически не могут менять области друг друга.
2. Test Worker по умолчанию не получает implementation context.
3. `critical` task не закрывается без назначенного high-risk proof plan.
4. Prompt injection из candidate/issue/tool output не меняет policy или capabilities.

## P3. Scale and learning

Состав:

- module/dependency matrix и parallel waves;
- конфликтные зоны; разработчик остаётся single merge coordinator;
- model routing profiles и production evals;
- framework learning loop;
- installer/update flow, дополнительные agent adapters и опциональные remote delivery adapters.

P3 принят, если:

1. Scheduler не параллелит задачи с зависимостями или конфликтующими write zones.
2. Любой новый agent/adapter/model проходит role-specific eval и постепенное включение.
3. Изменения framework связаны с failure fixture или измеримой целью.
4. Один реальный проект прошёл несколько десятков задач с измеренными отказами, recovery и defect escape rate.
