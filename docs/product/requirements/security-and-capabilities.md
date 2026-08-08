# Безопасность и capabilities

## Role profiles, sandbox и hooks

**CAP-001 [P0].** Каждая роль MUST иметь декларативный profile: read paths, write paths, forbidden paths, tools, network policy, credentials, child-agent policy, timeout и budgets.

**CAP-002 [P0].** Default для сети, credentials, внешних инструментов и записи MUST быть `deny`.

**CAP-003 [P0].** До запуска Controller MUST проверять, что write boundary непустая, нормализована, находится внутри workspace и не пересекается с forbidden paths.

**CAP-004 [P0].** Runtime MUST превентивно запрещать запись вне boundary. Post-run diff check MUST дополнительно сверять фактический diff, но MUST NOT считаться заменой sandbox.

**CAP-005 [P0].** Implementation Worker MUST быть физически лишён записи в test zones; Verifier и Briefer — во всё project tree; Controller — в product code и tests.

**CAP-006 [P0].** Роль MUST NOT создавать исполняющие дочерние роли. Только Controller/Orchestrator может создавать исполнителей.

**CAP-007 [P1].** Не-Explorer роль MAY создавать только read-only Explorer в пределах отдельного cap. Explorer MUST NOT создавать агентов.

**CAP-008 [P0].** Trusted framework policy MUST загружаться из защищённой base ref или иной области, которую candidate не может изменить.

**CAP-009 [P0].** Task/spec input, candidate files, comments, test output, web content, tool output и данные внешних issue/PR adapters MUST считаться untrusted data и MUST NOT переопределять trusted policy.

**CAP-010 [P0].** Hook engine MUST выполнять pre/post checks на переходах и возвращать machine-readable verdict, stable code, evidence, owner и resume route.

**CAP-011 [P0].** Минимальные hooks: schema validation, recursive-delegation deny, write-boundary enforcement, tool allowlist, budgets, secret scan, spec/SHA binding и stable halt codes.

**CAP-012 [P1].** Capability broker MUST быть единственным путём для credentials и local branch/commit. Capability локального merge или мутации внешней продуктовой системы MUST отсутствовать.

**CAP-013 [P1].** Credentials MUST выдаваться на конкретную роль и действие, не наследоваться всем run и не попадать в model context/evidence.

**CAP-014 [P0].** Настроенный sandbox backend MUST ограничивать path traversal, symlinks, запись вне workspace, дочерние процессы, сеть и чтение host credentials. Запуск агента без успешно применённого sandbox profile MUST завершаться до выдачи контекста.

**CAP-015 [P2].** Architectural hooks SHOULD проверять запрещённые imports, обход data owner/public API, зависимости shared-layer и ссылки на эфемерные документы.

**CAP-016 [P3].** Новый hook SHOULD допускаться только при воспроизводимом failure, дешёвой детерминированной проверке, низком false-positive rate, владельце и recovery route.

**CAP-017 [P0].** Sandbox MUST быть скрыт за версионируемым backend-neutral contract: preflight, create, read/write mounts, process policy, egress allowlist, spawn/cancel/kill, inspect и destroy.

**CAP-018 [P0].** Run/evidence MUST фиксировать sandbox backend ID/version, effective profile и результат preflight без чувствительных host details.

**CAP-019 [P0].** Если выбранный sandbox backend отсутствует или не способен обеспечить требуемый profile, Stepan MUST завершить запуск с `SANDBOX_UNAVAILABLE`; автоматический fallback в unsandboxed mode запрещён.
