# Stepan: план реализации итерации 0

Статус: готов к ревью  
Основание: [спецификация итерации 0](specification.md), редакция 2  
Целевая среда: Windows native  
Timebox: 2–3 рабочих дня одного разработчика

## 1. Цель плана

За timebox получить минимальный App Server probe и фактический ответ, пригоден
ли `codex app-server` версии `0.147.0` как approval-driven transport для
итерации 1.

План не переопределяет требования спецификации. Если обязательная гипотеза не
подтверждается, результатом этапа становится `BLOCKED` с минимальным evidence,
а не обход через experimental API, broad permissions или эвристику.

## 2. Исходное состояние

В репозитории уже есть legacy spike для `codex exec`:

- `cmd/codex-probe` и `internal/codexexec`;
- subprocess lifecycle, Windows Job Object, raw artifacts и replay-тесты;
- durable state primitives;
- `internal/gitsnapshot` с Git-native candidate snapshot.

Этот код не переименовывается и не переделывается в App Server client. Новый
двусторонний transport реализуется рядом в `internal/agentruntime/codexapp`. Из legacy-кода
переносятся только небольшие уже проверенные приёмы; общий framework выделяется
только при фактической необходимости.

На момент составления плана подтверждены `codex-cli 0.147.0` и наличие команды
`codex app-server`; `go` отсутствует в `PATH`. До первой реализации необходимо
обнаружить или установить Go `1.26.x` и записать точную patch-версию в evidence.

## 3. Ограничения реализации

- Только stdio JSONL; WebSocket, daemon и remote transport не реализуются.
- Только стабильная поверхность протокола без `experimentalApi` в обязательном
  пути.
- Только стандартная библиотека Go и уже имеющийся `golang.org/x/sys/windows`.
- Без CLI framework, универсального JSON-RPC framework, code generation в Go,
  provider interface и recovery engine.
- Реальный Codex не вызывается из `go test ./...`; все protocol failures сначала
  воспроизводятся fake subprocess и replay fixtures.
- Решение модели не является evidence для sandbox, Git или process lifecycle.
- Live-запуски используют отдельные временные репозитории без remote,
  credentials и ссылок на исходный checkout.
- `.stepan/` и несаницированные live artifacts не коммитятся.
- `internal/codexexec` удаляется или сохраняется только по итоговому решению в
  отчёте.

## 4. Ожидаемое размещение

```text
cmd/codex-appserver-probe/
internal/agentruntime/codexapp/
internal/agentruntime/codexapp/testdata/
internal/gitsnapshot/
docs/specs/iteration-0/
  implementation-plan.md
  codex-integration-contract.md
  report.md
```

В `internal/agentruntime/codexapp` должны появиться только необходимые ответственности:
запуск процесса, JSONL/JSON-RPC framing, correlation, thread/turn lifecycle,
approval policy, durable state и artifacts. Разбиение по файлам выполняется по
мере роста кода, а не заранее по одному типу на файл.

Generated protocol schema сначала хранится в `.stepan/spike/`. В репозиторий
добавляется её hash и команда воспроизведения; сам bundle коммитится только если
после ручной проверки он нужен для воспроизводимости и не содержит локальных
данных.

## 5. Этапы реализации

### Этап 0. Baseline и schema gate

Результат: версия CLI и фактический protocol contract воспроизводимо
зафиксированы до написания клиента.

- [ ] Обнаружить Go `1.26.x`, зафиксировать `go version` и согласовать `go.mod`.
- [ ] Сохранить `codex --version` и `codex app-server --help` в live evidence.
- [ ] Выполнить `codex app-server generate-json-schema --out <temp-dir>`.
- [ ] Вычислить SHA-256 bundle, записать версию CLI, argv и инструкцию
  воспроизведения.
- [ ] По generated schema выписать точные формы `initialize`, config, thread,
  turn, terminal notifications и трёх approval requests.
- [ ] Проверить, какие поля доступны без `experimentalApi`; несовпадение со
  спецификацией сразу отметить как риск или `BLOCKED`.
- [ ] Зафиксировать исходный `git status`, не перезаписывая пользовательские
  изменения и существующие `.stepan` artifacts.

Контрольная точка: schema генерируется версией `0.147.0`, имеет сохранённый hash
и согласуется с минимальным обязательным handshake. Иначе реализация transport
не начинается.

### Этап 1. JSONL/JSON-RPC ядро и fake App Server

Результат: один parser и одна state machine обрабатывают fake, replay и будущий
live stream.

- [ ] Реализовать envelope request/response/notification без универсальной
  поддержки всех методов App Server.
- [ ] Хранить ID как opaque scalar и использовать type-tagged canonical key для
  correlation.
- [ ] Выделять каждую непустую UTF-8 строку как один JSON object с лимитом
  16 MiB.
- [ ] Сериализовать все client writes через одного writer.
- [ ] Регистрировать client requests и server requests до обработки response.
- [ ] Отклонять orphan/duplicate response, duplicate request ID,
  противоречивые terminal notifications и turn с unresolved approval.
- [ ] Обрабатывать `serverRequest/resolved` как отдельный lifecycle signal как
  до, так и после отправки client response.
- [ ] Сохранять неизвестный валидный notification; неизвестный request
  завершать fail-closed.
- [ ] Реализовать fake App Server текущим Go test binary без mock framework.
- [ ] Покрыть handshake error, approvals, concurrent requests, malformed и
  oversized lines, stderr flood, зависание и unresolved approval.

Проверка: `go test ./internal/agentruntime/codexapp` проходит без установленного Codex и без
сети.

Контрольная точка: replay и fake subprocess используют тот же parser, writer и
state machine, что будет использовать live probe.

### Этап 2. Subprocess lifecycle и artifacts

Результат: probe безопасно держит двустороннее stdio-соединение и всегда
оставляет диагностируемый результат.

- [ ] Запускать executable напрямую с массивом аргументов и разрешать его через
  абсолютный путь или `PATH`, исключив текущий каталог.
- [ ] Открывать stdin/stdout/stderr до `Start`, читать stdout и stderr
  конкурентно, stdin держать открытым до завершения connection.
- [ ] Назначать App Server в Windows Job Object и ограниченно дочитывать pipe
  после cancel.
- [ ] Создавать `manifest.json`, raw `stdout.jsonl`, `stderr.log`, normalized
  events, approvals journal, `state.json` и записываемый последним `result.json`.
- [ ] Назначать normalized events единый монотонный `seq`, `event_id`, UTC
  timestamp и доступные correlation IDs; порядок между разными OS pipes считать
  порядком наблюдения, а не причинным порядком.
- [ ] Заменять `state.json` атомарно только после успешного flush/close журналов.
- [ ] Сохранять raw stdout до semantic parsing, а permission payload
  санитизировать до durable записи.
- [ ] Различать spawn failure, process failure, protocol failure, timeout,
  approval timeout и operator cancel.
- [ ] Проверить fake child → grandchild, унаследованный открытый pipe, оба
  потока больше системного pipe и warning в stderr.

Контрольная точка: fake process tree гарантированно исчезает после timeout и
cancel; partial evidence остаётся читаемым.

### Этап 3. Handshake, thread/turn и structured output

Результат: пройден минимальный вертикальный путь `A01`, затем resume `A02` и
классификация `A16`.

- [ ] Отправлять ровно один `initialize`, проверять response и отправлять
  `initialized`.
- [ ] Читать effective config и `configRequirements/read`; fail-closed при
  несовместимых требованиях.
- [ ] Выполнять `thread/start` или `thread/resume` только по явному ID.
- [ ] Запускать turn с explicit `cwd`, `approvalPolicy=onRequest`,
  `sandboxPolicy.type=readOnly`, restricted readable roots и `outputSchema`.
- [ ] Сохранять thread/turn/item IDs только из соответствующих protocol fields.
- [ ] Определить по schema и `A01` точный path structured final output и
  зафиксировать его без извлечения JSON из текста.
- [ ] Валидировать тестовый объект typed-декодированием с запретом неизвестных
  полей и проверкой `result`, `nonce` и required fields.
- [ ] После перезапуска App Server возобновить точный thread и подтвердить
  предыдущий context отдельным nonce.

Контрольная точка: `A01`, `A02` и `A16` имеют `PASS`, `FAIL` или `BLOCKED`, raw
evidence и внешние postconditions; `INCONCLUSIVE` не допускается.

### Этап 4. Durable approval state и policy

Результат: command, file-change и permissions requests получают ровно одно
решение только после durable фиксации.

- [ ] Реализовать минимальные состояния `starting`, `ready`, `running_turn`,
  `awaiting_controller_decision`, `awaiting_operator` и terminal states.
- [ ] Перед ответом сохранить pending request с request/thread/turn/item IDs,
  kind, policy snapshot и candidate snapshot.
- [ ] Нормализовать roots, path patterns, command form и `cwd`; проверять
  allowlist точным сравнением, без исполнения shell parser.
- [ ] Автоматически отклонять protected paths, network без разрешения,
  destructive/unknown commands, session grants и расширение scope.
- [ ] Поддержать одноразовые `accept`, `decline`, `cancel` и внутренний
  `await_operator`; записывать `decision_source`.
- [ ] Не блокировать stdout reader ожиданием оператора: request передаётся
  state machine, чтение stream продолжается.
- [ ] Перед отправкой решения повторно вычислять policy/candidate snapshot;
  изменившийся request инвалидировать.
- [ ] Защитить operator continuation переходом `pending → decided → sent`,
  допускающим отправку ровно один раз.
- [ ] После restart или потери connection завершать старый pending request
  fail-closed; повторять turn только отдельным явным действием.
- [ ] После `accept` проверять files, `HEAD`, `git status` и отсутствие изменений
  вне разрешённой области.

Контрольная точка: fake/replay тесты подтверждают порядок «durable decision →
response → observable operation» и fail-closed поведение при потере connection.

### Этап 5. Write approvals и operator delegation

Результат: live-сценарии `A03`–`A06` подтверждают управляемость материальных
операций.

- [ ] Для каждого сценария создать отдельный временный Git-репозиторий и
  sibling-каталог с уникальными nonce marker.
- [ ] Подтвердить одноразовый `accept` разрешённой записи (`A03`).
- [ ] Подтвердить `decline` внешней записи и отсутствие дополнительных прав
  (`A04`).
- [ ] Сверить proposed file changes до решения и независимо проверить принятый
  и отклонённый paths (`A05`).
- [ ] Остановить turn в `awaiting_operator`, сохранить решение и отправить его
  ровно один раз (`A06`).
- [ ] Пересчитать candidate snapshot до approval и после принятой операции.

Контрольная точка: ни одна запись не происходит до записанного решения, а
запрещённая или оставленная без ответа операция не выполняется.

### Этап 6. Restricted read и role isolation

Результат: `A07`–`A11` доказывают отсутствие доступа test-author к production
source и возможность узкого turn-scoped grant.

- [ ] Создать физически отдельный test-only Git-репозиторий без remote, objects,
  alternates, worktree links, symlink/junction и hardlink к исходному checkout.
- [ ] Копировать туда только allowlist contract/test inputs и не переносить
  project instructions, `.codex`, `.agents`, hooks, plugins или MCP config.
- [ ] Разместить source canary в sibling-каталоге вне readable roots.
- [ ] Передать restricted `ReadOnlyAccess` с явными roots и проверить
  разрешённое чтение (`A07`).
- [ ] Проверить прямую и command-mediated попытку чтения canary, затем найти
  nonce во всех model-visible outputs и artifacts (`A08`).
- [ ] Связать и отклонить source permission request (`A09`).
- [ ] Выдать только запрошенный безопасный read root со scope `turn`, проверить
  недоступность sibling source и отсутствие переноса grant (`A10`).
- [ ] Дать test-author создать тест из контракта, а отдельному verifier —
  выполнить его с source access и вернуть санитизированный результат (`A11`).

Контрольная точка: `A10` проходит без experimental API. Broad/session grant или
source leakage означает `BLOCKED` для итерации 1.

### Этап 7. Cancel, configuration isolation и Windows paths

Результат: завершены `A12`–`A15` и закрыты Windows-specific риски.

- [ ] Сначала отправлять `turn/interrupt`, затем по grace deadline закрывать Job
  Object; отдельно проверять execution timeout, approval timeout и operator
  cancel (`A12`).
- [ ] В isolated mode удалить унаследованные `CODEX_*`, кроме нужного
  `CODEX_HOME`, и передать framework-owned overrides явно.
- [ ] Проверить effective config, managed requirements и `instructionSources`;
  незаявленный источник считать failure (`A13`).
- [ ] Отдельно измерить влияние inherited user/project config, tools, hooks и
  MCP, не используя этот режим как default (`A14`).
- [ ] Повторить основной путь в каталогах с пробелами, кириллицей, `&`, `[` и
  `]`, не добавляя shell escaping (`A15`).
- [ ] Зафиксировать различия Windows/Linux/macOS только как теоретический
  анализ; cross-build не считать runtime evidence.

Контрольная точка: App Server и потомки не остаются после остановки, а isolated
mode не меняет auth, approval, sandbox, tools и output contract скрытым образом.

### Этап 8. Candidate snapshot и replay regression

Результат: существующий `internal/gitsnapshot` подтверждён как часть approval
contract без ненужной переработки.

- [ ] Прогнать существующие tracked, untracked, deletion, rename,
  staged+unstaged, ignored и Unicode cases.
- [ ] Добавить только отсутствующие проверки одинаковых size/timestamp,
  повторного расчёта и изменения во время capture.
- [ ] Проверить побайтовую неизменность настоящего index, `HEAD`, refs и working
  tree.
- [ ] Использовать `HEAD OID + tree OID` как candidate snapshot ID в approval
  fixtures.
- [ ] Повторно прогнать все санитизированные replay fixtures через production
  parser/state machine.

Контрольная точка: snapshot стабилен, обнаруживает изменение candidate и не
мутирует Git state.

### Этап 9. Contract, report и финальная проверка

Результат: spike можно вручную проверить и принять либо остановить по
зафиксированным причинам.

- [ ] Оставить минимальный набор санитизированных success, approval,
  process-failure и protocol-failure fixtures.
- [ ] После sanitization повторно проиграть fixtures и проверить отсутствие
  nonce canary, credentials и локальных абсолютных путей.
- [ ] Заполнить `codex-integration-contract.md` только фактически подтверждённым
  initialize/thread/turn/approval/restart поведением.
- [ ] Заполнить в `report.md` все 15 решений раздела 15 спецификации со ссылками
  на evidence и матрицу `A01`–`A16`.
- [ ] Отдельно решить судьбу legacy `codex exec` probe; до этого не удалять его
  код и raw observations.
- [ ] Сверить критерии готовности и условия блокировки строка за строкой.
- [ ] После утверждения результата отдельным изменением синхронизировать
  `docs/product-brief.md` и `docs/implementation-roadmap.md`.

Финальные команды:

```powershell
go test ./...
git diff --check
git status --short
```

Контрольная точка: тесты не обращаются к Codex, каждый `A01`–`A16` имеет
однозначный итог, contract/report прошли ручное ревью, а в Git нет live
`.stepan` artifacts или secrets.

## 6. Порядок по дням

| День | Этапы | Обязательный результат |
|---|---|---|
| 1 | 0–3 | Schema gate, fake transport, process lifecycle, `A01`, `A02`, `A16` |
| 2 | 4–5 и cancel из 7 | Durable approvals, `A03`–`A06`, fake/replay timeout tests |
| 3 | 6–9 | `A07`–`A15`, snapshot regression, fixtures, contract и report |

Если ранняя контрольная точка не пройдена, оставшееся время используется на
минимальное воспроизведение, sanitization evidence и запись решения `BLOCKED`,
а не на расширение реализации.

## 7. Матрица live-сценариев

| Сценарии | Этап | Основное evidence |
|---|---:|---|
| `A01`, `A02`, `A16` | 3 | Raw stream, IDs, terminal/final output, stderr/process classification |
| `A03`–`A05` | 5 | Approval journal, files, `HEAD`, `git status`, candidate snapshots |
| `A06` | 5 | Durable pending state и единственный отправленный response |
| `A07`–`A10` | 6 | Read policy, filesystem denial, permission response, canary scan |
| `A11` | 6 | Изолированный test repo и санитизированный verifier result |
| `A12` | 7 | Interrupt timeline, partial state и отсутствие process tree |
| `A13`, `A14` | 7 | Effective config, requirements и instruction sources |
| `A15` | 7 | Manifest и успешные postconditions в Windows paths |

## 8. Definition of done

План выполнен, когда одновременно:

- fake/replay тесты покрывают framing, correlation, approvals, malformed input,
  timeout и process tree;
- live matrix `A01`–`A16` заполнена без `INCONCLUSIVE` на пути итерации 1;
- acceptance/decline и restricted read доказаны внешними наблюдениями;
- operator decision durable и отправляется не более одного раза;
- structured output извлекается только по version-specific protocol path;
- isolated config и role workspace не пропускают запрещённые источники;
- candidate snapshot не меняет настоящий Git state;
- `go test ./...` проходит offline;
- integration contract, report и решение о переходе к итерации 1 готовы к
  ручному утверждению.
