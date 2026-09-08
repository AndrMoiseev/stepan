# ADR 0003: macOS/arm64 и containment через process group

Статус: **принято**

Дата: 2026-08-24

## Контекст

ADR 0001 принял Windows/amd64 как единственную платформу MVP и Windows Job
Object как обязательную границу дерева `codex app-server`. Для нативной работы
на Apple Silicon нужен эквивалентный lifecycle, но macOS не предоставляет Job
Object с `KILL_ON_JOB_CLOSE`.

Простой `Process.Kill` завершает только непосредственный процесс. ACP SDK также
не решает эту задачу: он задаёт агентский протокол, но запуск, сигналы и
containment процесса остаются ответственностью клиента. Stepan продолжает
использовать version-specific Codex App Server transport.

## Решение

### Матрица платформ

- Поддерживаются Windows native/amd64 и macOS native/arm64.
- Intel Mac (`darwin/amd64`) и Linux остаются неподдерживаемыми.
- Обе проверки platform preflight используют общую матрицу из
  `internal/platformsupport`.
- Pinned-версия остаётся `codex-cli 0.147.0` на обеих платформах.

### Общий lifecycle supervisor

`internal/processjob` предоставляет три операции:

1. `Prepare(*exec.Cmd)` до запуска процесса;
2. `Assign(*os.Process)` сразу после успешного `Start` и до protocol operation;
3. повторно вызываемый `Close()` для принудительного завершения containment.

`internal/agentruntime/codexapp` использует этот lifecycle. Ошибка любой операции
является fail-closed.

На Windows `Prepare` не меняет команду, `Assign` помещает процесс в Job Object,
а `Close` закрывает handle с `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.

На macOS:

- `Prepare` задаёт `SysProcAttr.Setpgid = true` и `Pgid = 0`, поэтому новая
  process group создаётся в дочернем процессе до `exec`;
- ожидаемый PGID равен PID лидера и сверяется после старта;
- PGID `0`, `1` и собственная process group Stepan никогда не используются как
  цель сигнала;
- `Close` отправляет `SIGKILL` через `kill(-pgid, ...)` всей группе;
- `ESRCH` означает, что группа уже отсутствует, и считается успехом.

Штатное завершение App Server остаётся двухфазным: Stepan сначала best-effort
отправляет `turn/interrupt` и ждёт до трёх секунд, затем закрывает supervisor.

### Терминал

На macOS интерактивность stdin/stdout проверяется через
`github.com/charmbracelet/x/term`. Redirected поток отклоняется до запуска
Codex. Существующая Windows console-проверка сохраняется.

## Граница гарантии

Darwin process group завершает App Server и потомков, которые наследуют его
группу. Потомок может намеренно вызвать `setsid` или сменить process group и
выйти из containment. Кроме того, process group не имеет kernel-owned
`kill-on-parent-close`, поэтому аварийное завершение самого Stepan до выполнения
defer не даёт гарантии очистки.

Эта гарантия слабее Windows Job Object и принимается для текущего локального
интерактивного запуска доверенного pinned Codex. Stepan не должен описывать её
как security boundary против враждебного дочернего процесса.

## Рассмотренные альтернативы

### Завершать только непосредственный процесс

Отклонено: обычные потомки Codex могут остаться живыми после cancel или crash
App Server.

### Перейти на ACP SDK

Отклонено для этой задачи: смена протокола не предоставляет OS containment и
значительно расширяет границу изменения существующего рабочего App Server
контракта.

### Внешний supervisor или `launchd`

Отложено. Такой процесс может пережить crash Stepan и усилить cleanup guarantee,
но требует отдельного IPC, ownership, recovery и installation lifecycle.

## Последствия

- Одна codebase собирает отдельные executable для Windows/amd64 и macOS/arm64
  без CGO.
- Общий supervisor API требует настройки команды до `Start` во всех местах
  запуска Codex.
- Windows-семантика Job Object не ослабляется.
- macOS требует отдельной ручной приёмки на физическом Apple Silicon Mac;
  cross-build подтверждает только компиляцию.
- Signing, notarization, installer и macOS CI этим решением не вводятся.

## Критерии пересмотра

ADR пересматривается, если:

- обнаружен detached descendant после штатного shutdown;
- Stepan должен гарантировать cleanup после собственного crash;
- добавляется Intel Mac, Linux, daemon/service mode или недоверенный agent
  executable;
- Codex предоставляет поддержанный внешний lifecycle с более сильной гарантией;
- меняется pinned Codex transport или версия.

## Основания

- [ADR 0001](0001-codex-app-server-containment.md)
- [ADR 0002](0002-current-stack-and-architecture.md)
- [План совместимости с macOS](../changes/features/macos-compatibility/implementation-plan.md)
- [План ручной приёмки](../changes/features/macos-compatibility/manual-test-plan.md)
