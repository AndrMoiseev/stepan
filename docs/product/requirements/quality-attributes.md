# Нефункциональные требования

## Безопасность

- **NFR-SEC-001 [P0].** Default-deny для мутаций, сети, credentials и рекурсивного делегирования.
- **NFR-SEC-002 [P0].** Secrets MUST NOT попадать в model input, prompts, evidence, logs или commits.
- **NFR-SEC-003 [P0].** Candidate MUST NOT иметь возможность изменить trusted policy собственного review.
- **NFR-SEC-004 [P2].** Threat model MUST охватывать prompt injection через repository content, path traversal/symlink, credential exfiltration, network egress и cross-worker interference.

## Надёжность и целостность

- **NFR-REL-001 [P0].** Повтор команды после crash MUST либо быть доказанно идемпотентным, либо блокироваться до reconciliation.
- **NFR-REL-002 [P0].** Частично записанное состояние MUST обнаруживаться и не интерпретироваться как успешный переход.
- **NFR-REL-003 [P1].** Recovery MUST быть идемпотентным: повторный recovery без изменения внешних фактов не создаёт новые side effects.
- **NFR-REL-004 [P1].** Одновременно действующий lease на одну задачу MUST быть не более одного.

## Наблюдаемость и экономика

- **NFR-OBS-001 [P0].** Stepan MUST считать attempts, dispatches, elapsed time и hook hits; tokens/cost — если доступны.
- **NFR-OBS-002 [P1].** Метрики MUST различать роли, модели, task type, risk и failure code.
- **NFR-OBS-003 [P3].** Dashboard SHOULD показывать accepted tasks/attempts, median/p95 time and cost, parking causes, context resets, verifier disagreements, escaped defects, rollback rate и owner decisions.

## Расширяемость и переносимость

- **NFR-EXT-001 [P0].** Agent runtime, local Git, command runner и storage MUST иметь версионируемые contracts; подключение произвольного агента MUST быть публично документированной extension point.
- **NFR-EXT-002 [P0].** Role/policy configuration MUST проходить schema validation до запуска.
- **NFR-EXT-003 [P1].** Durable schemas MUST иметь migration strategy и обратную совместимость как минимум на одну поддерживаемую версию.
- **NFR-EXT-004 [P1].** Отказ одного provider-а MUST быть классифицирован как environment/provider failure и не повреждать durable state.

## Эксплуатация и приватность

- **NFR-OPS-001 [P0].** Стандартный run MUST корректно завершаться по cancel/timeout, сохраняя безопасный checkpoint.
- **NFR-OPS-002 [P1].** Пользователь MUST управлять местом хранения, retention и экспортом transcripts/logs/evidence.
- **NFR-OPS-003 [P1].** Stepan MUST явно показывать, какие данные передаются каждому agent adapter и во внешний provider/service, если он используется.
