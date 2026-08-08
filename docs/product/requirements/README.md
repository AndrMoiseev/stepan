# Каталог требований Stepan

Статус: нормативный набор требований  
Дата: 2026-08-08

Эти документы переводят методологию «Инженерия надёжных фреймворков на AI-агентах» в проверяемые требования к Stepan. Они описывают поведение продукта, системные инварианты, durable-артефакты и границы версий. Конкретная архитектура реализации фиксируется в ADR.

## Нормативные термины

- **MUST** — обязательное требование; без него соответствующий этап продукта не считается готовым.
- **SHOULD** — рекомендуемое требование; отклонение требует записанной причины.
- **MAY** — допустимое расширение.
- **P0–P3** — последовательные этапы поставки из [roadmap-and-acceptance.md](roadmap-and-acceptance.md).

Идентификатор требования глобален для всего набора и не зависит от имени файла. При переносе требования между направлениями его ID и смысл должны сохраняться.

## Направления

| Документ | Область | Семейства требований |
|---|---|---|
| [Фундамент, роли и workflows](foundation-and-workflows.md) | порядок авторитетности, инварианты, разделение ролей, подсистемы и workflow engine | `INV`, `WF` |
| [Спецификация и планирование](specification-and-planning.md) | governing specification, task plan, briefs и форматы | `PLAN`, `SPEC`, `FMT` |
| [Lifecycle, контекст и восстановление](orchestration-and-recovery.md) | task lifecycle, scheduler, бюджеты, memory, recovery, failures и learning | `LIFE`, `CTX`, `REC`, `FAIL`, `LEARN` |
| [Безопасность и capabilities](security-and-capabilities.md) | role profiles, sandbox, capability broker и hooks | `CAP` |
| [Исполнение, проверка и evidence](execution-verification-and-evidence.md) | worktrees, gates, risk, tests, verification, evidence и storage | `EXEC`, `RISK`, `TEST`, `VER`, `DONE`, `EVD`, `STATE` |
| [Delivery и локальный Git](delivery-and-git.md) | candidate commit, `merge_ready`, merge handoff и closure | `DEL` |
| [Adapters, модели и evals](adapters-models-and-evaluation.md) | agent adapter contract, provider policy, routing и evals | `MOD` |
| [Интерфейс и установка](operator-experience-and-installation.md) | локальный CLI, статус, halt UX, installer/update | `UX`, `INST` |
| [Нефункциональные требования](quality-attributes.md) | безопасность, надёжность, наблюдаемость, расширяемость, эксплуатация и приватность | `NFR-*` |
| [Этапы и критерии приёмки](roadmap-and-acceptance.md) | состав и end-to-end acceptance P0–P3 | — |

## Общие правила чтения

- Конституционные инварианты действуют для всех направлений и этапов.
- Ссылка `[P0]`–`[P3]` указывает первый этап, на котором требование обязательно.
- Требования разных файлов применяются совместно; локальный раздел не ослабляет общий invariant или quality attribute.
- При конфликте источников применяется `INV-001` из [foundation-and-workflows.md](foundation-and-workflows.md).

Связь с исходной методологией приведена в [traceability.md](traceability.md).
