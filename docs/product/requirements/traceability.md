# Трассировка к методологии

| Глава исходного документа | Реализация в требованиях |
|---|---|
| 1. Ненадёжные компоненты | продуктовый бриф, `INV-003`–`INV-005`, роли |
| 2. Исполняемая спецификация | `PLAN-001`–`PLAN-010`, `SPEC-001`–`SPEC-015`, `FMT-001`–`FMT-005` |
| 3. Роли и изоляция контекста | роли, `WF-001`–`WF-016`, `CAP-005`–`CAP-007`, `TEST-001`–`TEST-002` |
| 4. Workflow как state machine | `LIFE-001`–`LIFE-013` |
| 5. Hooks и permissions | `CAP-001`–`CAP-019`, `NFR-SEC` |
| 6. Независимая верификация | `TEST`, `VER`, `DONE`, `EVD-001`–`EVD-009`, `STATE-001`–`STATE-008` |
| 7. Контекст и восстановление | `CTX-001`–`CTX-009`, `REC-001`–`REC-004` |
| 8. Модели, экономика и evals | `MOD-001`–`MOD-020`, `NFR-OBS` |
| 9. Git и delivery | `EXEC-001`–`EXEC-007`, `DEL-001`–`DEL-009` |
| 10. Отказы и learning loop | `FAIL-001`–`FAIL-006`, `LEARN-001`–`LEARN-003` |
| 11. Минимальная архитектура | логические подсистемы, P0–P3, `UX` и `INST` |
| 12. Операционные чек-листы | admission, role preconditions, merge checks и acceptance scenarios P0–P3 |

## Следующие проектные артефакты

На основе нормативного набора следует подготовить:

- Product Requirements Document с утверждёнными пользователями, scope и acceptance scenarios;
- domain model и machine-readable schemas;
- ADR по controller/storage/sandbox/runtime/delivery architecture;
- threat model;
- P0 implementation plan и end-to-end test matrix.
