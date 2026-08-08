# Delivery и локальный Git

**DEL-001 [P0].** Local Git adapter MUST предоставлять операции чтения Git facts и создания branch/worktree/candidate commit. Операция merge не входит в capability Stepan.

**DEL-002 [P0].** Branch, worktree, task, attempt, lease, base SHA и candidate SHA MUST иметь однозначную связь.

**DEL-003 [P0].** Создание candidate commit MUST выполняться Controller/Capability broker, не worker-ом или verifier-ом. Локальный merge выполняет только разработчик вне агентного workflow.

**DEL-004 [P0].** Перед присвоением состояния `merge_ready` Controller MUST проверить target branch, approved candidate SHA, обязательные gates, blocking findings, dependencies, lease и rollback route.

**DEL-005 [P0].** После rebase, conflict resolution или любого изменения candidate SHA старые evidence/verdict/approval MUST быть инвалидированы.

**DEL-006 [P1].** Frozen modules и conflict zones MUST быть частью task policy. Падение в frozen zone MUST приводить к сохранению provenance и park/escalate, а не к несанкционированной правке.

**DEL-007 [P0].** После выполненного разработчиком локального merge Stepan MUST обнаружить его по Git facts, проверить соответствие approved candidate, записать merge SHA/delivery evidence, закрыть lifecycle, снять lease и безопасно очистить worktree согласно policy.

**DEL-008 [P3].** Parallel delivery MUST сохранять разработчика единственным merge coordinator и сериализовать миграции, lockfiles, public contracts, shared types, generated registries и deploy config, если независимость не доказана.

**DEL-009 [P0].** P0 MUST использовать fast-forward-only handoff для merge: Stepan сообщает разработчику точный approved candidate SHA и команду, но не выполняет её. Если target branch сдвинулась, кандидат MUST вернуться на rebase/reverification и не может быть закрыт на старом evidence.
