# Stepan: спецификация итерации 0

Редакция: 2 — approval-driven App Server spike

Статус: готово к ревью

Итерация: 0 — технический spike

Timebox редакции: 2–3 рабочих дня одного разработчика

Целевая среда: Windows native

Стек: Go

## 1. Назначение

Итерация должна экспериментально подтвердить, что Codex CLI можно использовать
как управляемый двусторонний subprocess для следующих итераций Stepan, не
реализуя собственный агентский runtime и не вызывая API моделей напрямую.

Основной проверяемый transport — `codex app-server` поверх stdio JSON-RPC.
Stepan выступает клиентом протокола, получает streamed events и запросы
подтверждения, проверяет их своей политикой, отвечает сам либо делегирует
материальное решение оператору.

`codex exec` больше не является основным transport будущего Stepan. Полученные
в первой редакции результаты и raw evidence сохраняются как доказательство
ограничений одностороннего non-interactive режима, но отрицательный `C06` сам по
себе не блокирует новую архитектуру.

Результатом является не production-ready controller, а минимальный App Server
probe, воспроизводимые fixtures и зафиксированный контракт интеграции.
Неизвестное поведение Codex нельзя маскировать эвристикой: оно должно завершить
соответствующий сценарий как неуспешный и попасть в отчёт.

## 2. Зафиксированные решения и допущения

- Probe и последующий Stepan реализуются на Go.
- Базовый toolchain — Go `1.26.x`; фактическая patch-версия записывается в
  `go.mod`, manifest и отчёт.
- Фактические проверки выполняются только в Windows native.
- MVP считается Windows-only, пока обязательные сценарии не пройдены на другой
  ОС. Теоретический анализ и cross-compilation не считаются runtime evidence.
- Базовая проверяемая версия — локально установленный `codex-cli 0.147.0`.
- Команда `codex app-server` в этой версии помечена experimental. Поддержка
  фиксируется только для точной фактически пройденной версии; автоматическая
  совместимость с более ранними или будущими версиями не предполагается.
- JSON Schema bundle протокола генерируется установленным CLI без флага
  `--experimental`; его hash и версия Codex входят в evidence.
- Основной transport — `codex app-server --stdio`. WebSocket, daemon и remote
  transport не входят в обязательный путь spike.
- Codex запускается напрямую с массивом аргументов. Shell, PowerShell,
  `cmd.exe`, строковая сборка команды и shell-escaping не используются.
- Запросы JSON-RPC передаются отдельными UTF-8 строками через stdin. stdout
  содержит отдельные JSON-RPC messages; stderr остаётся независимым каналом.
- Базовая позиция роли задаётся как `readOnly` с явно ограниченным read access,
  `approvalPolicy=onRequest` и reviewer `user`.
- `user` в конфигурации Codex означает маршрутизацию approval в клиент App
  Server. Фактическим источником решения может быть policy Stepan или оператор;
  источник всегда записывается в durable journal.
- Ни одна роль не получает безусловный `workspaceWrite` как default.
  Разрешение на конкретную запись или команду выдаётся только после проверки
  соответствующего server request.
- В MVP используются только одноразовые решения. Stepan автоматически не
  отправляет `acceptForSession`, session-scoped permission grants и
  `acceptWithExecpolicyAmendment`.
- `dangerFullAccess`, `danger-full-access` и обход approvals/sandbox запрещены.
- Permission profiles являются beta, а `additionalPermissions` в command
  approval — experimental. Основной критерий безопасности не зависит от них.
- Запрет чтения обеспечивается до запуска операции sandbox-границей или
  физически отдельным role workspace. Approval является механизмом узкого
  исключения, а не единственной защитой.
- Аутентификация берётся из существующей установки Codex. Credentials не
  копируются в репозиторий, fixtures, role workspace или отчёт.
- Probe использует стандартную библиотеку Go. Единственное заранее допустимое
  исключение — `golang.org/x/sys/windows`, если оно потребуется для Windows Job
  Object.

## 3. Основание в интерфейсе Codex

Официальная документация фиксирует необходимые механизмы:

- App Server предназначен для глубокой интеграции, включая conversation
  history, approvals и streamed agent events;
- stdio transport использует JSON-RPC messages, после `initialize` клиент
  отправляет `initialized`, создаёт или возобновляет thread и запускает turn;
- schema протокола можно сгенерировать из установленной версии CLI;
- `turn/start` поддерживает `outputSchema`, `approvalPolicy` и явный
  `sandboxPolicy`;
- `ReadOnlyAccess` позволяет заменить default `fullAccess` на restricted
  `readableRoots`;
- command и file changes могут приходить как server-initiated approval requests;
- встроенный `request_permissions` создаёт
  `item/permissions/requestApproval` для filesystem и network permissions;
- permission response может содержать только подмножество запрошенных прав и
  иметь turn или session scope;
- permission profiles позволяют задавать `read`, `write` и `deny`, но помечены
  beta.

Источники: [Codex App Server](https://learn.chatgpt.com/docs/app-server),
[Agent approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security),
[Permissions](https://learn.chatgpt.com/docs/permissions),
[Sandbox](https://learn.chatgpt.com/docs/sandboxing),
[Non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode).

Документация задаёт ожидаемое поведение, но не заменяет empirical-проверки
конкретной версии CLI на Windows.

## 4. Цели

Итерация отвечает на следующие вопросы.

1. Можно ли безопасно запустить App Server без shell, выполнить handshake и
   завершить процесс вместе со всем деревом потомков?
2. Можно ли строго разобрать JSON-RPC requests, responses и notifications,
   сохранив неизвестные валидные messages?
3. Можно ли создать thread, запустить turn, получить structured final output и
   продолжить явно выбранный thread после перезапуска App Server?
4. Можно ли перехватить command, file-change и permission approvals и связать
   каждый запрос с точными `threadId`, `turnId`, `itemId` и request ID?
5. Можно ли принять разрешённую запись один раз, отклонить запрещённую и
   подтвердить результат по файловой системе и Git, а не по сообщению агента?
6. Можно ли приостановить turn, сохранить pending approval, показать его
   оператору и продолжить только после явного решения?
7. Можно ли ограничить чтение роли явным набором roots и доказать, что закрытый
   source marker не попал в model-visible output или evidence?
8. Можно ли дать test-author доступ к контракту и каталогу тестов без доступа к
   production source, а проверки выполнить отдельным verifier/controller?
9. Можно ли выдать только запрошенное read-разрешение на один turn и не
   расширить остальные filesystem permissions?
10. Можно ли отличить отказ sandbox, отказ controller, cancel оператора,
    timeout, protocol error и падение процесса?
11. Можно ли изолировать framework-owned настройки от пользовательской
    конфигурации, не ломая аутентификацию?
12. Можно ли вычислить стабильный идентификатор candidate snapshot, не изменяя
    настоящий Git index и рабочее дерево?

## 5. Не входит в итерацию

- Полный CLI и продуктовый workflow Stepan.
- Workflow спецификации, планирования, реализации, rework и commit.
- Публичный runner/provider interface или второй агентский CLI.
- Универсальная реализация JSON-RPC всех методов будущих версий App Server.
- Автоматический reviewer agent и сложная risk-scoring система.
- Автоматическое расширение прав на session или постоянное изменение execpolicy.
- Зависимость обязательного пути от experimental `additionalPermissions`.
- Production-ready recovery из любой точки protocol exchange.
- Полная OS-виртуализация, container runtime или общий ACL-manager.
- Гарантия, что любой отказ чтения автоматически породит approval request:
  обычная операция может завершиться отказом без `request_permissions`.
- Поддержка macOS, Linux или WSL.
- CI, installer, update-механизм, telemetry и продуктовые метрики.
- Оптимизация стоимости модельных запусков.

Минимальная физическая изоляция test-author входит в spike: отдельный каталог
без production source и без общей Git object database. Это не объявляется
универсальным isolation-механизмом будущих ролей.

## 6. Артефакты итерации

После выполнения должны существовать:

1. Go probe, запускающий реальный или fake App Server по одному пути исполнения.
2. Автоматические тесты subprocess и JSON-RPC слоя без обращения к Codex.
3. Санитизированные fixtures requests, responses, notifications и approvals.
4. Сгенерированный установленным CLI JSON Schema bundle протокола либо его
   проверяемый hash и инструкция воспроизведения.
5. JSON Schema тестового финального ответа.
6. Внутренний контракт интеграции с App Server.
7. Отчёт о spike с матрицей сценариев, наблюдениями и решениями.
8. Явная role access matrix для implementation, test-author и verifier.
9. Решение о поддерживаемой ОС и точной версии Codex.

Рекомендуемое минимальное размещение после реализации:

```text
cmd/codex-appserver-probe/
internal/agentruntime/codexapp/
internal/agentruntime/codexapp/testdata/
docs/specs/iteration-0/codex-integration-contract.md
docs/specs/iteration-0/report.md
```

Legacy probe удалён после успешного App Server spike и не является частью
текущей структуры репозитория.

Live-артефакты сохраняются в `.stepan/spike/` и не коммитятся. В репозиторий
попадают только небольшие санитизированные fixtures и version-specific schema,
если ручное ревью подтвердит отсутствие secrets и локальных путей.

## 7. Контракт App Server probe

### 7.1. Вход

Probe принимает:

- абсолютный путь к executable Codex либо имя `codex` для разрешения через
  `PATH`;
- абсолютный путь к role workspace;
- role ID и access policy;
- initial user input как UTF-8;
- JSON Schema финального ответа;
- execution timeout и отдельный approval timeout;
- каталог артефактов;
- необязательный явный thread ID для resume;
- режим конфигурации `isolated` или `inherited`.

Access policy содержит как минимум:

- нормализованные readable roots;
- нормализованные writable roots, доступные только для approval policy Stepan;
- запрещённые paths и path patterns;
- разрешённые command forms и working directories;
- признак допустимости network access;
- список решений, которые всегда требуют оператора.

Путь к executable после разрешения сохраняется в manifest. Probe не должен
случайно запускать `codex.exe` из текущего каталога.

### 7.2. Запуск и handshake

Логический subprocess-вызов:

```text
codex app-server
  --stdio
  --strict-config
  -c approvals_reviewer="user"
  <явные framework-owned config overrides>
```

Точный список поддерживаемых flags и config keys берётся из `--help` и schema
версии `0.147.0`; значения не собираются в shell-строку.

После запуска клиент обязан:

1. отправить ровно один `initialize` с идентификатором Stepan;
2. проверить успешный response и сохранить platform metadata;
3. отправить `initialized` notification;
4. вызвать `configRequirements/read` и проверить допустимость требуемых policy;
5. создать thread или возобновить его по явному ID;
6. вызвать `turn/start` с `approvalPolicy=onRequest`, explicit `cwd`,
   `outputSchema` и `sandboxPolicy.type=readOnly`;
7. передать restricted read access с явными `readableRoots`, а не полагаться на
   default `fullAccess`;
8. читать stream до terminal `turn/completed`, `turn/failed` или отмены.

`capabilities.experimentalApi` в обязательных сценариях не включается.
Отдельный exploratory-сценарий может проверить experimental поля, но его
результат не входит в критерий готовности основного пути.

### 7.3. Protocol messages

- Request содержит `method`, `params` и `id`.
- Response содержит тот же `id` и ровно одно из `result` или `error`.
- Notification не содержит `id`.
- ID считаются opaque JSON scalar в пределах разрешённой generated schema.
- Stepan не переиспользует client request ID на одном transport connection.
- Server request регистрируется как pending до отправки ответа.
- Ответ без соответствующего pending request, повторный response и
  противоречивые terminal notifications являются protocol failure.
- Валидный неизвестный method сохраняется как raw evidence. Он блокирует turn
  только если требует client response или меняет machine contract.
- Thread ID, turn ID, item ID и request ID не выводятся друг из друга.

### 7.4. Approval routing

Обязательные виды server requests:

- `item/commandExecution/requestApproval`;
- `item/fileChange/requestApproval`;
- `item/permissions/requestApproval`.

До ответа Stepan атомарно сохраняет pending approval минимум с полями:

```json
{
  "schema_version": 1,
  "request_id": "opaque-json-scalar",
  "thread_id": "opaque-string",
  "turn_id": "opaque-string",
  "item_id": "opaque-string",
  "kind": "command|file_change|permissions",
  "status": "pending",
  "requested_at": "RFC3339Nano UTC",
  "policy_snapshot_id": "opaque-hash",
  "candidate_snapshot_id": "opaque-hash-or-null"
}
```

Controller принимает одно из решений:

- `accept` — одноразовое разрешение после полной policy-проверки;
- `decline` — операция запрещена, turn может продолжить альтернативным путём;
- `cancel` — запрос или turn отменён оператором либо lifecycle controller;
- `await_operator` — внутреннее состояние Stepan; ответ App Server ещё не
  отправляется.

Каждое завершённое решение содержит `decision_source=policy|operator`, точное
решение, timestamp и ссылку на request. Молчание, timeout и потеря connection не
считаются согласием.

Автоматически запрещены:

- paths вне разрешённых roots;
- `.git`, `.stepan`, `.codex`, `.agents`, credentials и секретные файлы;
- network, если он явно не разрешён утверждённым планом;
- destructive или необратимые команды;
- неизвестный command form, shell nesting и несовпадающий `cwd`;
- расширение прав после изменения candidate или policy snapshot;
- `acceptForSession`, session-scoped grants и execpolicy amendment.

Command string является недоверенными данными. Автоматическое разрешение
возможно только для точно нормализованной формы из allowlist и ожидаемого `cwd`.
В остальных случаях запрос делегируется оператору либо отклоняется.

После `accept` Stepan всё равно проверяет наблюдаемый filesystem/Git result.
Approval доказывает авторизацию, но не корректность и не фактический scope
выполненной операции.

### 7.5. Ограничение чтения и role workspace

Default `readOnly` без explicit access неприемлем, потому что App Server может
дать ему `fullAccess` для чтения. Каждый turn получает restricted read access с
явным списком roots.

Минимальная role matrix spike:

| Роль | Доступ на чтение | Доступ на изменение |
|---|---|---|
| `implementation` | Утверждённая спецификация, план и разрешённая область репозитория | Только разрешённая область и только через approval |
| `test-author` | Публичный контракт, test requirements, существующие test utilities | Только каталог новых тестов и только через approval |
| `verifier` | Candidate snapshot, спецификация, diff и evidence | Нет |

Test-author запускается в отдельном test-only Git-репозитории:

- production source отсутствует физически;
- нет remote, общей Git object database, alternates, worktree pointer,
  symlink/junction или hardlink к исходному checkout;
- копируются только явно перечисленные contract/test inputs;
- секретный source canary находится в sibling-каталоге вне readable roots;
- project-owned instructions из исходного checkout не включаются в context;
- raw output и artifacts проверяются на отсутствие canary.

Если test execution требует чтения production source, его выполняет отдельный
verifier/controller. Test-author получает только структурированный и
санитизированный результат. Передача raw compiler output допускается лишь после
проверки, что он не раскрывает запрещённое содержимое.

Попытка запрещённого чтения может завершиться обычным sandbox denial и не обязана
автоматически создавать approval. Агенту разрешено вызвать `request_permissions`,
но безопасность не зависит от того, сделал ли он это.

Для `item/permissions/requestApproval` Stepan:

1. нормализует все абсолютные paths;
2. отклоняет не запрошенные или более широкие permissions;
3. разрешает только подмножество request;
4. использует только turn scope;
5. повторно проверяет role policy и snapshot перед ответом;
6. делегирует оператору доступ к source, credentials и иным закрытым roots.

Permission profiles можно проверить отдельным beta-сценарием. Основной сценарий
использует `ReadOnlyAccess` и физический role workspace, чтобы не зависеть от
beta-конфигурации.

### 7.6. Configuration isolation

Режим `isolated` обязан:

- удалить из child environment унаследованные `CODEX_*`, кроме необходимого
  для существующей аутентификации `CODEX_HOME`;
- передать framework-owned overrides явно;
- установить `approvals_reviewer=user`;
- запросить effective config через `config/read`, сохранив только
  санитизированные релевантные поля;
- запросить managed requirements через `configRequirements/read`;
- проверить, что требуемые `onRequest` и `readOnly` разрешены;
- выполнять базовые сценарии в workspace без `.codex`, `.agents`, `AGENTS.md`,
  hooks, plugins и MCP-конфигурации;
- проверить `instructionSources`, возвращённые thread API, и остановиться, если
  загружен неразрешённый источник.

`CODEX_HOME` и user config не считаются изолированными только потому, что probe
передал overrides. Любое неявное влияние на approval, sandbox, tools, hooks,
MCP или output contract должно быть либо измерено и запрещено, либо привести к
`BLOCKED`.

Режим `inherited` нужен только для сравнительного live-сценария и не является
default будущего Stepan.

### 7.7. Успешность turn

App Server является долгоживущим процессом, поэтому exit code процесса не
является сигналом успеха отдельного turn.

Turn успешен, только если одновременно:

1. handshake завершён;
2. thread и turn однозначно идентифицированы;
3. stream является валидным JSON-RPC;
4. все server requests получили ровно один допустимый response;
5. отсутствуют unresolved approvals;
6. наблюдался один непротиворечивый terminal status успеха;
7. final output найден по подтверждённому protocol path и независимо прошёл
   JSON Schema validation;
8. turn не был отменён и не превысил execution timeout;
9. postconditions сценария подтверждены внешними наблюдениями.

Если App Server завершился, его exit code и stderr учитываются отдельно. Exit
`0` не превращает незавершённый turn в успешный; ненулевой exit всегда является
process failure.

### 7.8. Артефакты одного сценария

```text
<artifact-dir>/
  manifest.json
  stdout.jsonl
  stderr.log
  normalized-events.jsonl
  approvals.jsonl
  state.json
  result.json
```

- `manifest.json` содержит версии Codex и protocol schema, OS/arch, executable,
  санитизированные paths, argv, role и hashes input/policy.
- `stdout.jsonl` сохраняет исходные bytes stdout без переформатирования.
- `stderr.log` сохраняет stderr отдельно.
- `normalized-events.jsonl` содержит события Stepan с correlation IDs.
- `approvals.jsonl` хранит request lifecycle и decision source без secrets.
- `state.json` является атомарным snapshot состояния scenario.
- `result.json` записывается последним атомарной заменой временного файла.
- Prompt и permission payload по умолчанию не дублируются в manifest; хранятся
  test case ID и hashes.

## 8. Требования к subprocess и JSON-RPC слою

### 8.1. Запуск без shell

- Используется `os/exec.Command` или `CommandContext` с отдельными аргументами.
- stdout и stderr подключаются к разным pipe до `Start`.
- stdin остаётся открытым для всего JSON-RPC lifecycle.
- stdout и stderr вычитываются конкурентно с момента запуска.
- JSON-RPC writes сериализует один writer; конкурентная запись bytes запрещена.
- Reader не блокируется ожиданием решения оператора: server request передаётся
  state machine, а чтение следующих messages продолжается.

### 8.2. JSONL framing

- Каждая непустая строка stdout должна быть самостоятельным JSON object.
- Максимальный размер одной строки — 16 MiB.
- Невалидная или оборванная строка сохраняется как evidence и завершает
  connection как protocol failure.
- Валидный неизвестный notification сохраняется без потери.
- Неизвестный request, требующий response, приводит к fail-closed ответу, если
  generated schema допускает такой response, либо к остановке connection.
- Порядок сохраняется строго внутри stdout и stderr. Общий `seq` означает
  порядок наблюдения Stepan, а не причинный порядок разных OS pipes.

### 8.3. Отмена и timeout

- Execution timeout, approval timeout и operator cancel являются разными
  причинами остановки.
- Пока Stepan находится в `await_operator`, execution timer может быть
  приостановлен, но действует отдельный approval deadline.
- Сначала отправляется `turn/interrupt`, если connection остаётся рабочим.
- Если bounded grace period истёк, закрывается Windows Job Object со всем
  деревом процессов.
- Повторная отмена идемпотентна.
- Partial artifacts и pending decision не удаляются.
- После потери connection pending approval считается отменённым. Автоматически
  повторять ранее принятое, но не подтверждённое решение запрещено.

## 9. Fake subprocess и replay-тесты

Fake App Server должен уметь:

- выполнить корректный initialize handshake;
- вернуть JSON-RPC error до initialize;
- создать thread и turn;
- выдать command, file-change и permissions approval requests;
- принять `accept`, `decline` и `cancel`;
- прислать `serverRequest/resolved` до и после client response;
- выдать два server requests одновременно;
- прислать duplicate request ID или response без request;
- завершить turn с unresolved approval;
- выдать неизвестный notification;
- записать warning в stderr при успешном turn;
- заполнить stdout и stderr больше размера системного pipe;
- вывести invalid/truncated/oversized JSON line;
- зависнуть до execution или approval timeout;
- породить дочерний и внучатый процессы;
- закрыть parent, оставив pipe открытым у потомка.

Тесты используют стандартный Go test binary как helper process. Replay fixtures
проходят через тот же parser и state machine, что live output.

## 10. Обязательные живые сценарии App Server

Каждый сценарий получает стабильный ID, ожидаемый результат и каталог evidence.

| ID | Сценарий | Проверяемое ожидание |
|---|---|---|
| `A01` | Handshake и базовый turn | Initialize успешен, thread/turn IDs сохранены, terminal success и structured output валидны |
| `A02` | Resume явного thread ID | После перезапуска App Server возобновлён точный thread; контекст предыдущего turn подтверждён nonce |
| `A03` | Разрешённая запись | App Server запросил approval; Stepan ответил одноразовым `accept`; marker создан только в разрешённой области; HEAD не изменён |
| `A04` | Запрещённая внешняя запись | Запрос наблюдался и получил `decline`; sibling marker отсутствует; turn не получил иных полномочий |
| `A05` | File-change policy | Предложенные paths доступны до решения; разрешённый change принят, защищённый path отклонён |
| `A06` | Делегирование оператору | Turn остаётся pending без исполнения; решение оператора сохраняется и ровно один раз отправляется App Server |
| `A07` | Restricted read — разрешённые inputs | Test-author читает contract/test inputs и не читает ничего вне readable roots |
| `A08` | Restricted read — source canary | Прямые и shell-попытки чтения source получают denial; canary отсутствует во всех model-visible outputs и artifacts |
| `A09` | Read permission decline | `item/permissions/requestApproval` связан с turn; controller отклоняет source access; содержимое не раскрыто |
| `A10` | Read permission grant subset | Разрешён ровно один безопасный input на один turn; sibling source остаётся недоступным; grant не переносится в следующий turn |
| `A11` | Blind test-author + verifier | Test-author создаёт тест только из контракта; отдельный verifier запускает проверку с source access и возвращает санитизированный результат |
| `A12` | Timeout и cancel | Turn ограниченно прерывается, pending approvals закрыты, дерево процессов отсутствует, partial evidence сохранено |
| `A13` | Configuration isolation | Effective config, requirements и instruction sources соответствуют framework policy; auth работает |
| `A14` | Inherited comparison | Зафиксировано влияние user/project config, tools, hooks, MCP и managed requirements |
| `A15` | Пути и UTF-8 | Role workspace и artifacts в пути с пробелами, кириллицей, `&`, `[` и `]` проходят без shell escaping |
| `A16` | Process stderr и terminal signals | Warning в stderr не меняет успешный turn; process failure и protocol failure классифицируются отдельно |

### 10.1. Ограничение модельных запусков

Сценарии разрешается объединять, если один invocation даёт независимое evidence
для каждой строки. Fake и replay не заменяют `A01`–`A16`, но покрывают все
искусственно воспроизводимые отказы до live-запусков.

Сценарий `A10` обязателен для возможности approval-gated чтения. Если версия
`0.147.0` не может выдать узкий turn-scoped filesystem grant через стабильный
protocol, результат фиксируется как `BLOCKED`; experimental fallback не выдаётся
за подтверждение основного пути.

### 10.2. Безопасность workspace

- Каждый write-сценарий выполняется в отдельном временном Git-репозитории без
  credentials и remotes.
- Sibling-каталог находится рядом с workspace, но не внутри системного temp.
- Test-only workspace не является worktree исходного репозитория и не разделяет
  с ним objects.
- Все marker/canary содержат уникальный nonce.
- Проверяются фактические files, `HEAD`, `git status` и отсутствие nonce в
  запрещённых output channels.
- Не используются production credentials, network или необратимые эффекты.

## 11. Structured final output

Для позитивных сценариев применяется небольшая schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "result": { "type": "string", "enum": ["ok"] },
    "nonce": { "type": "string", "minLength": 1 }
  },
  "required": ["result", "nonce"],
  "additionalProperties": false
}
```

Schema передаётся через `turn/start.outputSchema`. Точный protocol path, по
которому возвращается итоговый объект версии `0.147.0`, определяется generated
schema и `A01`; извлечение из произвольного текста или последней встреченной JSON
подстроки запрещено.

Независимая проверка выполняется typed-декодированием Go с запретом неизвестных
полей и явной проверкой required/enum/minLength.

## 12. Candidate snapshot

Spike сохраняет Git-native механизм, не меняющий настоящий index:

1. создать временный index вне `.git/index`;
2. установить `GIT_INDEX_FILE` только для дочерних команд Git;
3. загрузить `HEAD` через `git read-tree HEAD`;
4. применить working tree через `git add -A` во временный index;
5. получить tree OID через `git write-tree`;
6. представить snapshot как `HEAD OID + tree OID`.

Обязательные случаи: tracked, untracked, deletion, rename, staged+unstaged,
ignored, Unicode path, одинаковые size/timestamp с разным содержимым, повторный
расчёт и изменение во время расчёта.

Snapshot пересчитывается перед approval и после принятой операции. Если
candidate или policy snapshot изменился во время pending approval, запрос
инвалидируется и не принимается автоматически.

Механизм не должен менять refs, настоящий index или working tree. Известные
ограничения submodules, symlinks и file modes на Windows фиксируются явно.

## 13. Durable state и журнал

### 13.1. Формат

- Авторитетный snapshot: `.stepan/runs/<run-id>/state.json`.
- Append-only журнал: `.stepan/runs/<run-id>/events.jsonl`.
- Raw artifacts: `.stepan/runs/<run-id>/invocations/<invocation-id>/`.
- Все структуры имеют `schema_version`.
- Каждое событие имеет монотонный `seq`, `event_id`, UTC timestamp, `run_id`,
  необязательные `task_id`, `invocation_id`, `thread_id`, `turn_id`, `item_id`,
  `request_id`, `type` и `payload`.
- `state.json` заменяется атомарно после успешного flush/close.

### 13.2. Approval state

State machine содержит минимум:

```text
starting
→ ready
→ running_turn
→ awaiting_controller_decision
→ awaiting_operator
→ running_turn
→ completed | interrupted | failed
```

Pending approval записывается durable до ответа App Server. После restart
Stepan не предполагает, что старый stdio request остаётся живым. Если protocol
не предоставляет подтверждённый recovery path, pending request завершается
fail-closed, а turn возобновляется или повторяется отдельным явным действием.

Журнал не хранит credentials, полный environment, raw secrets и содержимое
запрещённых файлов. Raw permission payload перед сохранением санитизируется.

## 14. Теоретическая кроссплатформенность

Отчёт сравнивает Windows, Linux и macOS минимум по темам:

- разрешение executable и JSON-RPC stdio framing;
- Unicode paths;
- process groups, signals и завершение дерева;
- закрытие унаследованных handles;
- атомарная замена state;
- file modes, symlinks, junctions и hardlinks;
- `CODEX_HOME`, config layering и instruction sources;
- enforcement restricted read access и permission profiles;
- behavior Ctrl+C и pending approvals.

Cross-build не меняет Windows-only статус. OS-specific код ограничивается
process management и атомарной заменой; parser, state machine, policy и snapshot
остаются платформенно нейтральными.

## 15. Решения, обязательные в отчёте

`docs/specs/iteration-0/report.md` должен дать явное решение по пунктам:

1. Поддерживаемая ОС и точная версия Codex.
2. Пригодность experimental App Server для pinned-version MVP.
3. Точный initialize/thread/turn contract.
4. Извлечение structured final output.
5. Resume и restart behavior.
6. Correlation и lifecycle server requests.
7. Policy автоматического accept/decline и делегирования оператору.
8. Restricted read access и доказательство отсутствия source leakage.
9. Возможность turn-scoped read grant без experimental API.
10. Configuration isolation и managed requirements.
11. Timeout, cancel и завершение process tree.
12. Durable approval state и recovery pending request.
13. Candidate snapshot.
14. Известные Windows-ограничения и кроссплатформенные риски.
15. Судьба legacy `codex exec` probe и evidence.

Для каждого решения записываются наблюдение, выбранный вариант, отклонённые
варианты, последствия и ссылка на evidence.

## 16. Критерии готовности

Итерация завершена, когда одновременно:

- `go test ./...` проходит без реального обращения к Codex;
- fake App Server покрывает framing, approvals, malformed messages, timeout и
  дерево процессов;
- все `A01`–`A16` имеют однозначный итог;
- сценарии основного пути итерации 1 не имеют `INCONCLUSIVE`;
- handshake, structured output и resume подтверждены live evidence;
- разрешённая операция выполняется только после записанного decision;
- запрещённая операция и операция без ответа не выполняются;
- pending operator approval можно продолжить ровно один раз;
- restricted read access подтверждён filesystem evidence;
- source canary отсутствует в model-visible output и artifacts test-author;
- `A10` подтверждает узкий turn-scoped read grant без experimental API;
- test-author не имеет source access, а verifier выполняет проверку отдельно;
- timeout и cancel не оставляют App Server и потомков;
- effective config, requirements и instruction sources проверены;
- candidate snapshot стабилен и не меняет настоящий index;
- integration contract и report прошли ручное ревью;
- репозиторий не содержит credentials и live `.stepan/` artifacts;
- roadmap итерации 1 не требует неизвестного behavior App Server.

## 17. Условия блокировки итерации 1

Переход к walking slice блокируется, если выполняется хотя бы одно условие:

- App Server version-specific schema нельзя сгенерировать или согласовать с
  фактическими messages;
- невозможно однозначно связать request, decision, item, turn и thread;
- разрешённая запись не выполняется после одноразового `accept`;
- запрещённая операция выполняется до решения, после `decline` или вне scope;
- turn завершается успешно с unresolved approval;
- operator decision нельзя durable сохранить и отправить ровно один раз;
- restricted read access допускает чтение source canary;
- запрещённое содержимое попадает в prompt, instructions, output или artifacts;
- узкий read grant требует broad/session permission либо experimental API;
- user/project/managed configuration незаметно меняет approval, sandbox, tools
  или output contract;
- structured final output нельзя извлечь без эвристики;
- resume явного thread ID не работает либо возобновляет другой context;
- App Server или потомки остаются после timeout/cancel;
- candidate snapshot меняет index или не обнаруживает изменения.

Ошибки отдельных prompts, модели и сети не блокируют архитектуру автоматически,
если controller корректно маршрутизирует их как наблюдаемые отказы.

## 18. Порядок выполнения

### День 1

- Зафиксировать baseline `codex app-server --help` и generated JSON Schema.
- Реализовать минимальный stdio JSON-RPC transport и fake App Server.
- Проверить handshake, basic turn, structured output и process lifecycle.
- Выполнить `A01`, `A02`, `A16`.

### День 2

- Реализовать pending approval state и policy decisions.
- Выполнить write, decline и operator-delegation сценарии `A03`–`A06`.
- Реализовать Windows cancel/timeout и replay fixtures.

### День 3

- Создать физически отдельный test-only workspace.
- Выполнить restricted-read сценарии `A07`–`A11` и isolation `A13`–`A15`.
- Перепроверить candidate snapshot.
- Санитизировать fixtures, обновить integration contract и report.

Timebox не продлевается ради полной полировки. Если обязательная гипотеза не
подтверждена, отчёт фиксирует `BLOCKED` с минимальным evidence и вариантами
следующего решения.

## 19. Трассировка к roadmap

| Требование итерации 0 | Разделы спецификации |
|---|---|
| Запуск без shell и process lifecycle | 7.2, 8.1, 8.3, 10 (`A12`, `A16`) |
| JSON-RPC и streamed events | 7.3, 8.2, 9, 10 (`A01`) |
| Structured final response | 7.7, 10 (`A01`), 11 |
| Resume | 7.2–7.3, 10 (`A02`) |
| Approval routing | 7.4, 9, 10 (`A03`–`A06`) |
| Restricted read access | 7.5, 10 (`A07`–`A10`) |
| Role isolation test-author/verifier | 7.5, 10 (`A11`) |
| Configuration isolation | 7.6, 10 (`A13`, `A14`) |
| Paths и Windows behavior | 8.3, 10 (`A15`), 14 |
| Durable state и approval journal | 7.4, 13, 15 |
| Candidate snapshot | 7.4, 12, 15 |
| Поддерживаемая ОС и версия Codex | 2, 14–17 |

## 20. Влияние на связанные документы

После утверждения этой спецификации отдельным изменением должны быть приведены в
соответствие:

- `docs/product-brief.md`: заменить безусловный `workspace-write` на
  role-scoped approval routing и уточнить границу физической изоляции;
- `docs/implementation-roadmap.md`: заменить `codex exec` как основной transport
  на App Server и добавить pending approval state;
- `docs/specs/iteration-0/implementation-plan.md`: заменить этап 7 и матрицу
  `C01`–`C12` на `A01`–`A16`;
- `docs/specs/iteration-0/report.md`: сохранить `BLK-001` как legacy observation
  и открыть новый раздел решения по App Server.

Эти документы и код не изменяются в рамках записи данной спецификации.
