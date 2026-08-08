# Adapters, модели и evals

## Agent Adapter Contract

**MOD-001 [P0].** Открытый Agent Adapter Contract MUST позволять отдельному adapter process обернуть произвольного агента — CLI, API или service — без изменения Controller и канонического workflow.

**MOD-002 [P0].** Run record MUST сохранять adapter ID/version, agent ID/version, provider/model при наличии, role, prompt/profile revision, runtime settings и объявленные capabilities.

**MOD-003 [P0].** Канонические task/brief/verdict/evidence/error schemas MUST быть независимы от агента, способа запуска и провайдера модели.

**MOD-004 [P1].** Fallback MUST быть явной policy: retry, alternate agent/model, park или owner decision. Тихая смена агента или модели после исчерпания бюджета запрещена.

## Model routing и evals

**MOD-005 [P3].** Routing profile SHOULD выбираться по роли, риску, бюджету и собственным evals, а не по единому рейтингу моделей.

**MOD-006 [P3].** Независимые роли SHOULD разводить коррелированные blind spots разными model families, context surfaces или prompts; использование одной модели MUST быть наблюдаемым фактом.

**MOD-007 [P3].** Eval suite SHOULD измерять first-pass acceptance, rework count, hook hits/100 actions, false gaps, defect escapes, accepted-change cost, context resets, mutation score и verifier false positives.

**MOD-008 [P3].** Сравнение моделей SHOULD хранить стратификацию задач, prompt/tool revisions, настройки, failure classes и достаточность выборки.

## Протокол, изоляция и установка adapters

**MOD-009 [P0].** Adapter manifest MUST объявлять protocol version, executable/arguments, supported input/output schemas, streaming/events, cancellation, tool mediation, usage reporting и health check.

**MOD-010 [P0].** Agent adapter MUST NOT расширять capabilities роли. Любое чтение, запись, tool call, сеть или credential проходят через Capability broker независимо от возможностей агента.

**MOD-011 [P0].** Adapter SDK/protocol MUST документировать lifecycle вызова, structured result, timeout/cancel, crash recovery и отображение adapter-specific ошибок в стабильные failure codes Stepan.

**MOD-012 [P0].** Stepan MUST обнаруживать несовместимую версию adapter protocol до запуска роли и завершать preflight стабильной диагностикой.

**MOD-013 [P0].** Agent adapter MUST запускаться отдельным дочерним процессом внутри sandbox и общаться с ядром только через framed JSON Lines messages по stdin/stdout. In-process agent plugins MUST NOT поддерживаться.

**MOD-014 [P0].** Настроенный cloud agent adapter MAY передавать provider-у только scoped context, выданный роли Controller-ом. Сам факт включения adapter-а в project policy является разрешением на передачу без per-call confirmation; adapter MUST объявить network destinations, а sandbox MUST ограничить egress этим allowlist.

**MOD-015 [P0].** До передачи cloud provider-у контекст MUST пройти secret filtering. Audit record MUST хранить adapter/provider, task/role, timestamp и перечень переданных source paths/artifact IDs без дублирования полного исходного кода.

**MOD-016 [P0].** Agent adapter packages MUST устанавливаться глобально для пользователя в `~/.stepan/adapters/<adapter-id>/<version>/`; candidate worktree и агентные роли MUST иметь к этому каталогу только необходимый read/execute доступ либо не иметь прямого доступа вовсе при запуске через Controller.

**MOD-017 [P0].** Git-tracked project policy MUST связывать каждую роль с точными adapter ID/version, agent configuration и ожидаемым integrity digest; adapter executable MUST NOT храниться в project repository.

**MOD-018 [P0].** Adapter resolution MUST быть детерминированным. Отсутствующая exact version или mismatch integrity digest MUST останавливать preflight; implicit upgrade, downgrade или выбор `latest` запрещены.

**MOD-019 [P0].** CLI MUST предоставлять `adapter list` и `adapter doctor` либо эквивалентные операции для просмотра установленных версий, protocol compatibility, health и integrity.

**MOD-020 [P0].** Adapter становится доступным только после явной локальной установки разработчиком. Install record MUST сохранять source provenance, exact version и SHA-256 digest; cryptographic signature MAY поддерживаться, но не является обязательным admission gate.
