# Спецификация и планирование

## Генерация и ревью task plan

**PLAN-001 [P0].** Stepan MUST генерировать task plan из governing specification; разработчик не обязан вручную декомпозировать спецификацию в исполняемые задачи.

**PLAN-002 [P0].** Plan MUST содержать стабильные stage/task IDs, outcomes, acceptance IDs, dependency graph, risk, write boundaries, forbidden zones, proof paths и budgets. P0 исполняет полученный graph последовательно, даже если в нём видны потенциальные waves.

**PLAN-003 [P0].** Plan compiler MUST трассировать каждую task к governing spec revision и acceptance criteria и MUST регистрировать обнаруженные `SPEC_GAP`, а не заполнять их молча.

**PLAN-004 [P0].** До сохранения plan Stepan MUST детерминированно проверить schema, уникальность IDs, существование ссылок, отсутствие циклов dependencies, достижимость outcomes и совместимость write/frozen boundaries.

**PLAN-005 [P0].** CLI MUST позволять разработчику просмотреть весь plan, dependency graph, риски, gaps и proof paths, а также запросить replan с комментариями.

**PLAN-006 [P0].** Plan review разработчиком является доступным, но не обязательным gate. Stepan MAY исполнять валидный plan без review, если в нём нет material decision или policy-required approval; молчание разработчика не закрывает такие решения.

**PLAN-007 [P0].** Любая правка или replan MUST создавать новую plan revision и инвалидировать затронутые briefs, leases, evidence applicability, verdicts и approvals.

**PLAN-008 [P0].** Ручная правка Git-tracked plan разработчиком MAY использоваться как review correction, но после неё plan MUST пройти те же schema/semantic checks; отдельный hand-authored execution path в обход Plan compiler запрещён.

**PLAN-009 [P0].** После успешной генерации и валидации Controller MUST создать отдельный Git commit с generated plan. Commit SHA MUST стать governing plan revision для task compilation и последующего `run`; точная branch strategy определяется ADR.

**PLAN-010 [P0].** Review plan MUST поддерживать оба механизма: прямой patch YAML разработчиком и structured/free-text review comments для автоматического replan. В обоих случаях Stepan MUST создать новую plan revision, повторить полную валидацию и показать semantic diff относительно предыдущего plan.

## Спецификация и task compilation

**SPEC-001 [P0].** Stepan MUST регистрировать governing specification revision для каждой задачи.

**SPEC-002 [P0].** Модель-читаемая спецификация MUST поддерживать:

- наблюдаемое пользовательское поведение;
- входы, выходы и failure states;
- инварианты данных и безопасности;
- scope и anti-goals;
- совместимость и публичные контракты;
- acceptance criteria со стабильными ID;
- ожидаемый путь доказательства;
- открытые вопросы и владельца каждого решения.

**SPEC-003 [P0].** Admission validator MUST отклонять задачу без проверяемого outcome, governing revision, acceptance criteria, risk, write boundary, proof path, budget или terminal route.

**SPEC-004 [P1].** Stepan MUST поддерживать трассировку:

`intent → specification/AC → architecture/module → task → brief → code/tests → evidence → verdict`.

**SPEC-005 [P1].** Task compiler MUST строить brief как одностороннюю производную конкретной ревизии и MUST NOT превращать brief во второй источник истины.

**SPEC-006 [P1].** Brief MUST содержать как минимум: task ID, purpose, success outcome, governing requirement IDs, spec revision, архитектурные ограничения, разрешённые и запрещённые пути, proof plan и open decisions.

**SPEC-007 [P1].** Изменение governing specification MUST инвалидировать зависимые briefs, незавершённые verdicts и approvals.

**SPEC-008 [P1].** Worker, не способный вывести требуемое действие из brief, MUST вернуть `CONTEXT_INSUFFICIENT`, а не расширять scope.

**SPEC-009 [P1].** Briefer MUST отличать отсутствующий контекст от отсутствующего требования:

- если решение есть в governing source — выпустить исправленный brief;
- если решения нет или источники противоречат друг другу — зарегистрировать `SPEC_GAP`;
- если решение существенно — запросить Owner;
- закрыть gap только изменением авторитетного источника.

**SPEC-010 [P2].** Изменение спецификации SHOULD проходить loss-prevention check, обнаруживающий молчаливое удаление или смысловое ослабление существующих требований.

**SPEC-011 [P3].** Stepan SHOULD поддерживать матрицу модулей и зависимостей: responsibility, data owner, public API, providers, consumers, forbidden imports, shared/frozen/conflict zones.

**SPEC-012 [P0].** Авторитетные product/spec/policy sources MUST быть файлами, отслеживаемыми локальным Git. Governing revision MUST однозначно разрешаться в Git commit и набор content paths; mutable execution state не является governing source.

**SPEC-013 [P1].** Stepan MAY сформировать patch авторитетной спецификации в отдельном spec-change workflow, но MUST показать semantic diff и затронутые AC/tasks, получить явное подтверждение разработчика и только затем создать новую governing revision. Применение patch MUST инвалидировать зависимые briefs, evidence applicability, verdicts и approvals.

**SPEC-014 [P0].** Нормативным корнем продуктовых спецификаций MUST быть `<repository-root>/.stepan/spec/`. Functional requirement, не находящееся под этим корнем, не является governing source, пока явно не импортировано в нормативную спецификацию.

**SPEC-015 [P0].** `.stepan/spec/index.yaml` MUST перечислять normative Markdown documents, их document IDs, scope и зависимости. Plan compiler MUST читать спецификации только через validated index и MUST отклонять отсутствующие, дублирующиеся или выходящие за корень paths.

## Форматы артефактов

**FMT-001 [P0].** Human-authored intent, specification и decision records MUST храниться как UTF-8 Markdown со стабильными IDs и machine-readable metadata там, где это требуется schema.

**FMT-002 [P0].** Project policy, role bindings и generated task/plan declarations MUST храниться как UTF-8 YAML и проходить versioned schema validation.

**FMT-003 [P0].** Machine-produced verdicts и evidence manifests MUST храниться как canonical UTF-8 JSON с явной `schema_version`.

**FMT-004 [P0].** Cross-format references MUST использовать стабильные IDs и governing Git revision, а не относительный смысл заголовка или эфемерный filename.

**FMT-005 [P0].** Parser MUST отклонять duplicate keys/IDs, неизвестные обязательные enum values и schema version, которую текущая версия Stepan не поддерживает.
