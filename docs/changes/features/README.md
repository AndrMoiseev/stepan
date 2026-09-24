# Документы изменений

Каждая feature живёт в `docs/changes/features/YYYY-MM-DD-<feature-id>/`.
На этапе уточнения намерения Stepan создаёт `mem-log.md`; после первого draft
появляется единственный `intent.md`. Журнал append-only и хранит видимую
переписку, решения и review-события; агент не пишет оба project-файла.

`intent.md` описывает проблему, наблюдаемый результат, scope, exclusions и
существенные ограничения. Он не заменяет `specification.md`: полная
спецификация, дизайн и implementation plan могут появиться позднее отдельным
процессом.
