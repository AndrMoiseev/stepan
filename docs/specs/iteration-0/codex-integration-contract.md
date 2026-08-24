# Контракт интеграции Codex App Server 0.147.0

Статус: **принят для MVP с обязательными compensating controls ADR 0001**

Платформа: Windows native/amd64

Codex CLI: `0.147.0`

Transport: `codex app-server --stdio`

## Граница контракта

Этот документ описывает только пересечение трёх источников:

1. live-наблюдения `A01`–`A06` и `A16`;
2. generated JSON Schema установленного `codex-cli 0.147.0`;
3. поведение, которое реально реализует probe в `internal/codexapp`.

Generated bundle привязан к версии. Его manifest имеет SHA-256
`eb325d394d19f2f8d133203885b3d1c2f74dbc5a176f22078a4f99aae5926faa`;
ключевые bundle-файлы и их hashes перечислены в
[baseline manifest](baseline/codex-app-server-0.147.0/schema-bundle.sha256).
Совместимость с другой версией Codex не подразумевается.

Текущая [официальная документация Codex App Server](https://learn.chatgpt.com/docs/app-server)
(сверено 2026-08-11) подтверждает общую модель JSON-RPC, initialize,
thread/turn, streaming и approvals, но уже описывает более новую поверхность,
включая `readOnlyAccess`, новые permission profiles и дополнительные lifecycle
events. Эти возможности не являются контрактом `0.147.0`. В частности,
актуальная документация использует новые wire-значения вроде `onRequest`, тогда
как pinned schema и live-run `0.147.0` используют `on-request`.

## Transport и initialize

- Процесс запускается напрямую массивом аргументов:
  `app-server --stdio --strict-config -c approvals_reviewer="user"`. Shell не
  участвует.
- stdin/stdout — UTF-8 JSONL: один JSON-RPC message на строку, без обязательного
  поля `jsonrpc`. stderr читается независимо и не определяет исход turn.
- Client request содержит `method`, `params`, `id`; response — тот же `id` и
  ровно одно из `result`/`error`; notification не содержит `id`.
- На новом соединении Stepan первым отправляет один `initialize` с
  `clientInfo.name="stepan"` и непустой `clientInfo.version`. В обязательном
  пути `capabilities.experimentalApi` не отправляется.
- Успешный response обязан содержать непустые `codexHome`, `platformFamily`,
  `platformOs`, `userAgent`; затем клиент отправляет `initialized`.
- После handshake probe вызывает `configRequirements/read`. Если managed
  requirements явно запрещают `on-request` или `read-only`, запуск завершается
  как `config_incompatible`.

Evidence: [A01](results/A01.json), pinned `v1/InitializeParams.json` SHA-256
`6f0094be9a65242ec779a40794cbd4fdfa32fca1e45084a16adfb50501d33ea2`,
`v1/InitializeResponse.json` SHA-256
`62ad689c2cb6379913c1d72749cfd8de5089d35760214123518eb92eef11acc9`.

## Thread и turn

- Новый разговор: `thread/start` с явным `cwd`; продолжение:
  `thread/resume` с ранее сохранённым точным `threadId`.
- Response должен содержать непустой `thread.id`. Для resume он обязан точно
  совпасть с запрошенным ID.
- Затем отправляется `turn/start` с `threadId`, одним text input, явным `cwd`,
  `approvalPolicy="on-request"`, `sandboxPolicy={"type":"readOnly",
  "networkAccess":false}` и `outputSchema`.
- Response `turn/start` должен содержать непустой `turn.id`. Все последующие
  approval и terminal messages коррелируются независимо по request, thread,
  turn и item IDs; ID не выводятся друг из друга.
- Успешное завершение — ровно один `turn/completed` с совпадающими thread/turn
  IDs и `turn.status="completed"`. `interrupted` и `failed` не являются успехом.

В schema `0.147.0` вариант `readOnly` содержит только `type` и
`networkAccess`; поля restricted roots нет. Поэтому этот контракт не обещает
изоляцию чтения: `A07`/`A08` доказали обратное. Source-blind роли используют
физически отдельный workspace и OS boundary согласно
[ADR 0001](../../adr/0001-codex-app-server-containment.md).

Evidence: [A01](results/A01.json), [A02](results/A02.json), pinned
`v2/ThreadStartParams.json` SHA-256
`792e2f32e37cece971bd616664ea2053741acbed4e9c92e9d1766427718f2ecd`,
`v2/ThreadResumeParams.json` SHA-256
`7d9ff4b7d83702448715ada355a9713af6f71beaf6fcfcb08c4f03ac52842813`,
`v2/TurnStartParams.json` SHA-256
`ff2e7e0796fbe2ad99e5ec7d489cc8c8630b75f2ab8f17857711107587e3197d`.

## Structured final output

- Авторитетный path версии `0.147.0`, подтверждённый live:
  `turn/completed.params.turn.items[].agentMessage[phase=final_answer].text`.
- Должен присутствовать ровно один final agent message. Nullable `phase` есть в
  pinned schema; для совместимости probe принимает единственный agent message с
  отсутствующим phase только когда сообщений с `final_answer` нет.
- `text` декодируется как один JSON object с запретом неизвестных полей и
  trailing data. Для spike обязательны ровно `result="ok"` и ожидаемый непустой
  `nonce`.
- Поиск JSON-подстроки в произвольном тексте и выбор «последнего JSON» запрещены.

Evidence: [A01](results/A01.json), pinned
`v2/TurnCompletedNotification.json` SHA-256
`be5938b288e6b36b3f46585f2878f7a88150fc3f56fb373a6c961eea838cfdd6`.

## Approvals

- Live подтверждены server requests
  `item/commandExecution/requestApproval` и
  `item/fileChange/requestApproval`; pinned schema также объявляет
  `item/permissions/requestApproval`, но стабильный live lifecycle последнего в
  `0.147.0` не получен (`A09`/`A10`).
- Request ID — opaque string или int64. До ответа request записывается durable
  как pending вместе с точными thread/turn/item IDs, policy snapshot и candidate
  snapshot; command text, reasons, file contents и credentials не журналируются.
- Для file change предложенные paths берутся из коррелированного `item/started`;
  сам approval request может не содержать paths.
- Решение policy или оператора сохраняется до отправки. Финальный ответ
  отправляется ровно один раз. Разрешены только одноразовые `accept`, `decline`
  и `cancel`; автоматические `acceptForSession`, session grant и policy
  amendment запрещены.
- `accept` допустим только для точного allowlist и неизменных policy/candidate
  snapshots. Любая неоднозначность, protected path, network, glob или изменение
  snapshot превращает решение в `decline`.
- Успешный terminal message при unresolved approval считается protocol failure.

Evidence: [A03](results/A03.json), [A04](results/A04.json),
[A05](results/A05.json), [A06](results/A06.json), pinned approval response
hashes `6d0767113e22f311381809b6b236b0dde2b99b01992879c26bf7b1ea0e003cb7`,
`b95b03ee6be674e25cee2e863cc135a28620e1070addd2f34685aadee27cde08`,
`23f3f24e9dbf35db3e0b85703f0d934da5a1cff3cbb611fba8bcd41f3b4a04b0`.

## Restart и failure semantics

- После остановки одного App Server процесса новый процесс может выполнить
  `thread/resume` с сохранённым exact thread ID. `A02` подтвердил тот же ID,
  новый turn ID и восстановление контекста предыдущего turn.
- Pending stdio server request после потери соединения не считается живым и не
  восстанавливается. Локальное pending state закрывается fail-closed через
  `cancel`; дальнейший resume/retry требует отдельного явного действия.
- Ненулевой exit code всегда классифицируется как `process_failure`, даже если
  одновременно оборван protocol. Exit code 0 без валидного terminal message —
  `protocol_failure`. Непустой stderr при валидном terminal success не меняет
  исход.
- Гарантия timeout/cancel, `turn/interrupt` grace и завершения всего Windows
  process tree для App Server не подтверждена (`A12`). Она не входит в
  wire-контракт; iteration 1 обеспечивает её внешним Windows Job Object и
  bounded shutdown согласно ADR 0001.

Evidence: [A02](results/A02.json), [A12](results/A12.json),
[A16](results/A16.json).

## Обязательный operational envelope MVP

Принятие этого version-specific контракта не расширяет возможности App Server.
До каждого production turn Stepan обязан обеспечить внешние границы из
[ADR 0001](../../adr/0001-codex-app-server-containment.md):

- source-blind role workspace физически отделён, production source не доступен;
- dynamic read grants заменены заранее материализованными allowlisted inputs;
- App Server назначен в отдельный Windows Job Object до начала turn;
- cancel/timeout выполняет `turn/interrupt` → bounded grace → закрытие дерева;
- effective config, instruction sources, hooks, plugins и MCP совпадают с
  allowlist; credentials не копируются;
- нарушение любого preflight или containment invariant завершает run
  fail-closed.

Обычный Git worktree не является security boundary. Он может использоваться для
организации Git-изменений, но source isolation требует отдельной файловой/OS
границы.

## Явно не поддержано

- Любая версия Codex, кроме `0.147.0`, без нового schema diff и live replay.
- Linux/macOS runtime; WebSocket, daemon, remote transport и experimental API.
- Restricted read isolation или narrow read grant на stable API `0.147.0`.
- Полная config isolation при использовании существующего `CODEX_HOME`.
- Recovery незавершённого approval request между процессами.
- Production timeout/cancel/process-tree lifecycle App Server.

Изменение pinned версии требует заново сгенерировать bundle, сверить hashes,
прогнать fixtures и обязательные live-сценарии и выпустить новый contract.
