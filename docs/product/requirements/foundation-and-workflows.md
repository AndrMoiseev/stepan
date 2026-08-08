# Фундамент, роли и workflows

## Конституционные инварианты

Эти правила действуют на всех этапах продукта.

**INV-001. Порядок авторитетности.** При конфликте источников Stepan MUST применять порядок:

1. safety policy и явные решения оператора;
2. факты локального Git и явно подключённых внешних adapters;
3. governing-ревизия спецификации и задачи;
4. машинно произведённые evidence;
5. независимый verdict;
6. handoff роли;
7. snapshot scheduler-а;
8. разговорная память.

**INV-002. Одна продуктовая истина.** Спецификация MUST быть единственной авторитетной точкой входа для функционального поведения. Brief, handoff и чат являются производными или эфемерными артефактами.

**INV-003. Функциональное разделение.** Одна роль MUST иметь один измеримый выход и минимально необходимый контекст.

**INV-004. Независимость решения.** Исполнитель MUST NOT закрывать собственную задачу. Verifier MUST NOT изменять проверяемый кандидат.

**INV-005. Enforced least privilege.** Критичные запреты MUST обеспечиваться кодом ниже модели. Промпт не считается границей безопасности.

**INV-006. Revision binding.** Task, brief, evidence, verdict и approval MUST быть привязаны к governing spec revision; evidence, verdict и approval также MUST быть привязаны к candidate SHA.

**INV-007. No implicit gap filling.** Модель MUST NOT молча выбирать продуктовое поведение, отсутствующее в спецификации.

**INV-008. Bounded attempts.** Любая попытка и делегирование MUST иметь budget. Новая попытка MUST иметь новую информацию или записанную причину допустимого transient retry.

**INV-009. Recovery before new work.** После запуска controller MUST сначала восстановить и сверить незавершённое состояние и только затем выбирать новую задачу.

**INV-010. Safe stop.** При невозможности доказать безопасное продолжение Stepan MUST остановить или припарковать затронутую ветвь, сохранив факты и следующий безопасный шаг.

**INV-011. Risk proportionality.** Глубина gates, тестирования и верификации MUST зависеть от цены ошибки.

**INV-012. Replaceable models.** Замена модели или провайдера MUST NOT требовать изменения канонического lifecycle, evidence contracts и safety invariants.

**INV-013. Same candidate.** Stepan MUST NOT признать доставленным SHA, отличный от approved candidate SHA. Если разработчик изменил или слил другой кандидат, Stepan обязан обнаружить расхождение и потребовать новую верификацию.

**INV-014. Explicit material decisions.** Молчание владельца MUST NOT считаться согласием. Существенное решение принимает человек.

## Пользовательские роли

- **Operator / Owner** задаёт intent, спецификацию, допустимый риск и бюджет; принимает существенные решения и разрешает исключения.
- **Maintainer** настраивает framework policy, role profiles, hooks, adapters и eval-наборы. В P0 Operator и Maintainer могут быть одним человеком.

## Исполняемые роли

| Роль | Измеримый выход | Читает | Может писать | Не может |
|---|---|---|---|---|
| Controller | допустимый переход lifecycle | durable state, policy, local Git facts, evidence | scheduler state и разрешённые local Git commit operations | продуктовый код и тесты |
| Model Orchestrator | предложение следующего допустимого действия | план, состояние, verdicts | только структурированное предложение | самостоятельно применять переход |
| Briefer | revision-bound brief или gap | спецификация, архитектура, карта кода | эфемерный brief | дерево проекта и продуктовые решения |
| Explorer | структурированная карта найденных фактов | scoped repo/docs | только отчёт исследования | дерево проекта и любых агентов |
| Implementation Worker | candidate code diff или gap | brief и scoped code | разрешённые пути кода | тесты, lifecycle и delivery |
| Test Worker | независимый test diff | поведение, acceptance criteria, интерфейсы, fixtures | изолированная зона тестов | продуктовый код и lifecycle |
| Verifier | `PASS`, `REWORK`, `BLOCKED` или `OWNER_DECISION` | спецификация, кандидат, тесты, evidence | только verdict/findings | candidate branch |
| Adversarial Verifier | high-risk verdict/findings | критические контракты, кандидат, evidence | только verdict/findings | candidate branch |

Model Orchestrator является опциональной модельной ролью. Детерминированный Controller остаётся единственным владельцем фактических переходов.

## Обязательные логические подсистемы

Границы процессов и конкретная технология реализации определяются ADR, но целевая система MUST содержать следующие логические ответственности:

| Подсистема | Ответственность | Не должна делать |
|---|---|---|
| Product/Spec layer | хранить intent, requirements, AC, anti-goals и решения | хранить run status как вторую истину |
| Task compiler | строить revision-bound task и brief | принимать продуктовые решения |
| Controller/Scheduler | lifecycle, leases, budgets, recovery и допустимые переходы | писать продуктовый код/тесты |
| Agent/Runtime adapters | связывать произвольного агента с role/tool contract | менять канонический lifecycle под агента или провайдера |
| Sandbox/Capability broker | физически выдавать минимальные полномочия | доверять одному post-run анализу diff |
| Hook engine | выполнять детерминированные edge checks | заменять semantic verification |
| Evidence store | связывать facts, AC, spec revision и candidate SHA | принимать свободный рассказ за machine fact |
| Local Git adapter | представлять branch/worktree/commit и выполненный разработчиком merge как проверяемые операции и факты | выполнять merge или разрешать его агентной роли |
| Operator interface | показывать outcome/status/decision и принимать authority-bearing input | заставлять пользователя управлять внутренним зоопарком ролей |

Подсистемы MAY быть объединены в одном процессе P0. Их контракты и владение состоянием всё равно MUST оставаться различимыми и тестируемыми.

## Workflow engine и граница расширения

**WF-001 [P3].** Ядро Stepan MUST исполнять декларативные Git-tracked project workflow definitions без изменения Controller; целевая архитектура MUST NOT распределять orchestration semantics по provider/role-specific коду.

**WF-002 [P3].** Project workflow definition MUST иметь stable workflow ID, schema version, workflow revision, typed inputs/outputs, nodes, transitions, budgets и terminal outcomes.

**WF-003 [P3].** Configurable workflow node MUST поддерживать как минимум типы: agent role invocation, deterministic command/gate, hook, Controller operation, human decision и subworkflow call.

**WF-004 [P0].** Transition MUST зависеть от structured node result, stable failure/verdict code или детерминированного выражения над durable facts; свободный текст агента не может напрямую выбирать transition.

**WF-005 [P0].** Agent node MUST ссылаться на role profile, а role profile — на project binding adapter ID/version. Workflow MUST NOT содержать provider-specific executable/API logic.

**WF-006 [P0].** Workflow definition MUST быть скомпилирована и проверена до запуска: schema/types, ссылки, достижимость terminal outcomes, запрещённые cycles, budgets, authority escalation и совместимость capabilities.

**WF-007 [P0].** Workflow MUST NOT отключать constitutional invariants, sandbox, capability broker, trusted policy, evidence/spec/SHA binding, budget hard caps или Controller ownership lifecycle transitions.

**WF-008 [P0].** Каждый run MUST быть связан с immutable workflow ID/revision. Изменение workflow definition MUST NOT менять уже запущенный run молча; continuation требует совместимой migration или нового run.

**WF-009 [P0].** Stepan MUST поставлять фиксированный versioned catalog framework-owned flows. Flow definitions MUST проходить общий validator и использовать тот же execution mechanism, который позднее поддержит project-owned workflows.

**WF-010 [P0].** Стартовый reference workflow MUST разделять generation/commit plan и execution: `stepan plan` создаёт governing plan revision, а `stepan run` явно запускает исполнение. Project workflow MAY изменить эту композицию позднее, не меняя Controller.

**WF-011 [P3].** Subworkflow calls MUST иметь declared input/output contract, отдельный budget и максимальную глубину; динамическое создание неописанного workflow или рекурсивной власти запрещено.

**WF-012 [P3].** Workflow schema MAY выражать parallel branches только при доказанной dependency/write isolation; runtime всё равно применяет требования LIFE-012 и DEL-008.

**WF-013 [P3].** Project workflow definitions MUST храниться в `.stepan/workflows/` как YAML. Reference workflow MAY быть materialized туда с provenance/version при переходе проекта к configurable flows.

**WF-014 [P0].** P0 MUST разрешать только выбор flow ID/exact version из framework-owned catalog и настройку явно объявленных параметров. Project-defined flow files и custom node composition MUST отклоняться как неподдерживаемые.

**WF-015 [P0].** Реализация фиксированных flows MUST сохранять явные flow/step contracts и structured transitions, чтобы добавление configurable workflows позднее не потребовало изменения task/verdict/evidence schemas или safety invariants.

**WF-016 [P0].** Framework-owned catalog P0 MUST содержать ровно пять user-visible flows:

- `plan` — скомпилировать governing spec в validated plan и создать plan commit;
- `execute-task` — выбрать ready task, выполнить briefing/implementation/gates/verification/bounded rework и подготовить `merge_ready` candidate;
- `recover` — сверить local state с Git facts и безопасно продолжить, park или block;
- `spec-change` — предложить semantic spec patch, получить подтверждение разработчика и инвалидировать зависимые артефакты;
- `status` — показать outcome, progress/evidence, risk/budget, blockers и next safe action без изменения product state.

Добавление шестого user-visible flow в P0 требует новой product requirement revision.
