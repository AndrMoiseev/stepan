# Интерфейс оператора и установка

## Минимальный интерфейс

**UX-001 [P0].** Stepan MUST предоставлять эквиваленты операций:

- `start` — зарегистрировать intent/spec и подготовить первый walking slice;
- `run` — продолжить или восстановить допустимую работу;
- `status` — показать outcome, state, evidence, risk, budget и next action;
- `review` — получить и записать существенное решение Owner.

Конкретные имена CLI-команд могут отличаться.

Операции выше являются интерфейсом стартовых reference workflows. Они MUST быть реализованы через общий workflow engine, а не отдельными непереиспользуемыми ветками orchestration-кода.

**UX-002 [P0].** `status` MUST по умолчанию показывать человеческую карточку: outcome, state, progress по AC, last proof + SHA, risk, budget/attempt, нужен ли Owner, next action и возможность безопасного resume.

**UX-003 [P0].** Внутренние agent IDs, hook traces и state-machine details SHOULD быть доступны по drill-down/verbose, а не занимать основной статус.

**UX-004 [P0].** Все основные команды MUST поддерживать machine-readable output для автоматизации.

**UX-005 [P0].** Любой halt MUST объяснять code, факты, impact, owner и точную команду/условие возобновления.

**UX-006 [P1].** Dry-run/preview MUST показывать планируемые роли, capabilities, budgets, write boundaries, local Git mutations и передачу данных внешним agent providers до запуска.

**UX-007 [P0].** Основной workflow MUST полностью управляться из локального CLI и не требовать hosted account, daemon или удалённого control plane; сеть используется только явно настроенными agent adapters.

## Установка и обновления

**INST-001 [P3].** Installer MUST осмотреть target, показать preview, обнаружить коллизии до записи, подготовить staging, валидировать и атомарно применить либо откатить изменения.

**INST-002 [P3].** Framework-owned и project-owned files/state MUST быть разделены.

**INST-003 [P3].** Существующие project instruction files MUST NOT затираться молча; разрешены managed block, companion file или явный merge.

**INST-004 [P3].** Generated/managed artifacts MUST иметь provenance и версию генератора.
