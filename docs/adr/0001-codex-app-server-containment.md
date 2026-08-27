# ADR 0001: Codex App Server с внешней изоляцией и OS containment

Статус: **принято**

Дата: 2026-08-11

## Контекст

Итерация 0 подтвердила основной transport `codex app-server --stdio`,
structured output, resume, approvals, durable approval state и candidate
snapshot на Windows с `codex-cli 0.147.0`.

Сценарии `A07`–`A13` также зафиксировали ограничения этой версии:

- `readOnly` не ограничивает чтение заданными roots;
- stable narrow turn-scoped read grant недоступен;
- App Server path ещё не имеет гарантированного timeout/cancel/process-tree
  lifecycle;
- существующий `CODEX_HOME` загружает user config, hooks, plugins и MCP.

Это ограничения встроенной поверхности Codex, но необходимые границы можно
обеспечить снаружи средствами Stepan и ОС. Фактические результаты сценариев не
переименовываются: матрица остаётся `9 PASS / 4 FAIL / 3 BLOCKED`.

## Решение

Spike считается **пройденным с замечаниями**. Итерация 1 может использовать App
Server при выполнении следующих обязательных условий.

### Версия и transport

- MVP поддерживает Windows native/amd64 и pinned `codex-cli 0.147.0`.
- App Server запускается напрямую массивом аргументов, без shell.
- Generated schema, replay fixtures и live evidence являются частью
  version-specific контракта.
- Experimental API, broad/session grants и implicit permission expansion не
  используются.

### Изоляция чтения

- Source-blind роль запускается в физически отдельном role workspace, содержащем
  только заранее разрешённые contract/test inputs.
- Production source, исходный checkout и другие секретные каталоги не
  монтируются и не должны быть доступны security principal процесса.
- Обычный Git worktree или sibling-каталог **не считается границей
  безопасности**: worktree разделяет Git metadata, а соседние пути остаются
  читаемыми без OS-level ограничения.
- Пока stable narrow grant недоступен, динамическое расширение read scope
  запрещено. Оператор может одобрить копирование конкретного файла во вновь
  материализованный workspace и новый turn.
- Test-author и verifier запускаются раздельно. Verifier с source access
  возвращает только санитизированный результат.

### Завершение процесса

- Каждый App Server на Windows назначается в отдельный Job Object до начала
  turn. Если containment установить нельзя, run не начинается.
- При cancel или timeout Stepan сначала best-effort отправляет `turn/interrupt`,
  ждёт ограниченный grace period, затем закрывает Job Object со всем деревом.
- Direct `Process.Kill` не считается достаточным завершением дерева.
- Pending approvals закрываются fail-closed; partial state и artifacts
  сохраняются, а продолжение выполняется новым процессом через явный resume или
  retry.

### Конфигурация

- Для управляемых запусков используется отдельный доверенный профиль Codex,
  авторизованный штатным login flow; credentials не копируются между homes и не
  попадают в repository/evidence.
- Перед turn Stepan читает effective config, requirements и instruction sources,
  инвентаризирует hooks, plugins и MCP и сравнивает их с явным allowlist.
- Неизвестный или запрещённый источник завершает запуск fail-closed.
- Унаследованный пользовательский профиль допустим только как явно выбранный и
  записанный режим, а не как isolated default.

### Проверяемые инварианты

До dogfooding должны существовать автоматические проверки, что:

- source canary недоступен source-blind роли и отсутствует в model-visible
  output/artifacts;
- App Server и его потомки отсутствуют после cancel/timeout;
- effective config не содержит незаявленных hooks/plugins/MCP/instructions;
- approval decision durable записан до единственной отправки;
- настоящий Git index, refs и working tree не меняются при candidate capture.

## Последствия

- Итерация 1 разблокирована, но сначала реализует перечисленные containment
  gates; без них соответствующая роль завершается `BLOCKED`.
- Изоляция потребует дополнительного места и времени на материализацию role
  workspace.
- Dynamic narrow read grant временно заменён явным копированием inputs и новым
  turn.
- MVP остаётся Windows-only.
- App Server самостоятельно владеет проверкой версии Codex и lifecycle процесса.

## Критерии пересмотра

ADR пересматривается, если выполнено хотя бы одно условие:

- меняется pinned версия Codex, generated schema или wire contract;
- новая pinned версия предоставляет stable restricted roots или narrow
  turn-scoped grants, подтверждённые повтором `A07`–`A11`;
- native App Server lifecycle подтверждает interrupt/timeout/process-tree
  cleanup повтором `A12` и stress-проверкой;
- появляется поддержанный способ отделить auth от user config/plugins/MCP;
- добавляется Linux/macOS, параллельное выполнение или production credentials;
- стоимость отдельных workspaces становится измеримым bottleneck;
- canary/config/process containment invariant нарушается в dogfooding или
  инциденте.

При смене версии обязательны schema diff, replay всех fixtures и повтор затронутых
live-сценариев до обновления integration contract.

## Основания

- [Отчёт итерации 0](../changes/features/iteration-0/report.md)
- [Контракт Codex App Server 0.147.0](../changes/features/iteration-0/codex-integration-contract.md)
- [Результаты A01–A16](../changes/features/iteration-0/results/)
