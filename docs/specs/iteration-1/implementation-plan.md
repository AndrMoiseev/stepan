# Stepan: задачи реализации итерации 1

Статус: черновик для ревью  
Основание: [спецификация итерации 1](specification.md)  
Целевая среда: Windows native/amd64, `codex-cli 0.147.0`

## 1. Правила декомпозиции

- Одна задача ниже — один безопасный commit. После каждого commit проект
  собирается и `go test ./...` проходит без сети и настоящего Codex.
- Тесты изменения входят в тот же commit, что и production-код. Отдельные
  «дописать тесты позже» задачи не создаются.
- Следующая задача начинается только после выполнения критериев приёмки всех её
  зависимостей.
- Изменения ограничиваются указанной границей commit. Расширение границы требует
  повторного разбиения задачи.
- Существующие `cmd/codex-appserver-probe` и `internal/codexexec` остаются
  рабочими на каждом шаге.
- Общий workflow engine, provider interface, DSL, persistence, resume, raw
  artifacts и собственный TUI framework не создаются.
- Реальный Codex используется только в финальной live-проверке, не в
  автоматических тестах и не внутри implementation commit.

## 2. Порядок выполнения

| ID | Результат | Зависит от |
|---|---|---|
| I1-01 | Безопасные Git workspace и путь спецификации | — |
| I1-02 | Сравнение Git snapshots и write-boundary | — |
| I1-03 | Общий минимальный Windows Job Object helper | — |
| I1-04 | Управляемый процесс App Server и pinned preflight | I1-03 |
| I1-05 | Долгоживущее JSON-RPC соединение | I1-04 |
| I1-06 | Thread/turn API и structured final output | I1-05 |
| I1-07 | In-memory per-turn approval policy | I1-06 |
| I1-08 | Interrupt, shutdown и повторный запуск после сбоя | I1-04–I1-07 |
| I1-09 | Prompt contracts и строгие схемы результатов | I1-01 |
| I1-10 | Первоначальное уточнение идеи | I1-06, I1-09 |
| I1-11 | Атомарный сценарий создания первого черновика | I1-01, I1-02, I1-07, I1-10 |
| I1-12 | Вопрос по черновику и явное утверждение | I1-06, I1-09, I1-11 |
| I1-13 | Уточнение и применение изменения спецификации | I1-02, I1-07, I1-09, I1-12 |
| I1-14 | Интерактивные формы и команды | I1-10–I1-13 |
| I1-15 | Executable, lazy lifecycle и `Ctrl+C` | I1-08, I1-14 |
| I1-16 | Сквозные fake-сценарии и регрессионный gate | I1-15 |

## 3. Commit-задачи

### I1-01. Безопасные Git workspace и путь спецификации

**Результат:** `internal/specflow` умеет получить канонический корень Git и
без создания файлов проверить будущий `docs/specs/<spec-id>`.

**Граница commit:** `internal/specflow` и его тесты.

**Критерии приёмки:**

- Запуск из любого подкаталога возвращает тот же абсолютный канонический Git
  root.
- Запуск вне working tree завершается понятной ошибкой.
- Принимается только непустой `spec-id` длиной не более 64 символов из
  `[a-z0-9-]+`.
- Зарезервированные Windows device names отклоняются без учёта регистра.
- Существующий целевой каталог отклоняется и не изменяется.
- Путь отклоняется, если ближайший существующий родитель выводит цель из Git
  root через symlink, junction или другой reparse point.
- Проверка не создаёт целевой каталог и не меняет working tree.

**Чек-лист реализации:**

- [ ] Добавить получение root через `git rev-parse --show-toplevel` с прямым
  запуском `git`, без shell.
- [ ] Канонизировать root и ближайшего существующего родителя цели.
- [ ] Реализовать одну функцию валидации `spec-id`, включая `CON`, `PRN`, `AUX`,
  `NUL`, `COM1`–`COM9` и `LPT1`–`LPT9`.
- [ ] Вычислять абсолютный каталог и отображаемый slash-separated путь
  `docs/specs/<spec-id>/specification.md` только от Git root.
- [ ] Проверять отсутствие целевого каталога без его предварительного создания.
- [ ] Добавить повторно вызываемую проверку containment для использования после
  write-turn.
- [ ] Покрыть valid/invalid ID табличным тестом.
- [ ] Покрыть запуск из корня и вложенного каталога, существующую цель и Windows
  reparse point отдельными тестами.

**Проверка:** `go test ./internal/specflow` и `go test ./...`.

### I1-02. Сравнение Git snapshots и write-boundary

**Результат:** `internal/gitsnapshot` определяет только изменения, появившиеся
между двумя synthetic trees, и проверяет, что они лежат внутри одного
разрешённого каталога.

**Граница commit:** `internal/gitsnapshot` и его тесты.

**Критерии приёмки:**

- Исходно грязное дерево допустимо и не считается новым нарушением.
- Создание, изменение, удаление и rename после baseline обнаруживаются.
- Любое новое изменение вне разрешённого каталога возвращается в evidence и
  проваливает boundary check.
- Изменение внутри разрешённого каталога принимается.
- Смена `HEAD` во время операции проваливает проверку.
- Настоящие Git index, `HEAD`, refs и исходные пользовательские изменения не
  модифицируются.

**Чек-лист реализации:**

- [ ] Переиспользовать существующий `Capture`; не вводить второй механизм
  snapshot через разбор `git status`.
- [ ] Добавить сравнение `TreeOID` до и после через Git plumbing с NUL-delimited
  именами путей.
- [ ] Нормализовать полученные пути относительно Git root и не принимать
  абсолютные либо выходящие через `..` значения.
- [ ] Вернуть полный список новых changed paths для понятной ошибки, а не только
  boolean.
- [ ] Добавить boundary helper с одним разрешённым относительным root.
- [ ] Проверить неизменность `HeadOID` между snapshots.
- [ ] Добавить тест с заранее изменённым файлом вне allowed root и новой правкой
  только внутри root.
- [ ] Добавить тесты новых внешних файлов, rename/delete и одновременно
  внутренних и внешних изменений.
- [ ] Сохранить существующие проверки неизменности реального index и refs.

**Проверка:** `go test ./internal/gitsnapshot` и `go test ./...`.

### I1-03. Общий минимальный Windows Job Object helper

**Результат:** проверенный Job Object код переиспользуется `codexexec` и будущим
долгоживущим App Server без двух копий Windows API.

**Граница commit:** новый маленький `internal/processjob`, минимальная замена в
`internal/codexexec`, связанные тесты.

**Критерии приёмки:**

- Закрытие job завершает назначенный процесс и его потомков.
- Повторное закрытие безопасно.
- Ошибка создания или назначения job доступна вызывающему коду и не игнорируется.
- Поведение и тесты существующего `codexexec` не меняются.
- В helper нет timeout, signal handling или App Server-specific API.

**Чек-лист реализации:**

- [ ] Перенести существующие `newProcessJob`, `Assign` и `Close` в минимальный
  внутренний пакет с экспортом только нужных операций.
- [ ] Сохранить `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` на Windows.
- [ ] Сохранить простой process-kill fallback для non-Windows, чтобы пакет
  компилировался при обычных Go checks.
- [ ] Перевести `internal/codexexec` на новый helper без изменения публичного
  контракта runner.
- [ ] Перенести существующий process-tree тест к владельцу helper или добавить
  эквивалентный тест там, не дублируя медленный сценарий.
- [ ] Убедиться, что job создаётся до запуска управляемой операции и закрывается
  на всех error paths.

**Проверка:** `go test ./internal/processjob ./internal/codexexec` и
`go test ./...`.

### I1-04. Управляемый процесс App Server и pinned preflight

**Результат:** `internal/codexapp` запускает один contained
`codex app-server --stdio`, проверив платформу и версию до protocol turn.

**Граница commit:** process lifecycle в `internal/codexapp`, fake subprocess и
тесты; probe остаётся совместимым.

**Критерии приёмки:**

- Поддерживаются только Windows native/amd64 и точная версия `0.147.0`.
- `codex` разрешается через `PATH` без shell и без неявного выбора executable из
  текущего каталога.
- Version mismatch завершается до `thread/start`.
- App Server назначен Job Object до первой protocol operation; ошибка
  containment не позволяет начать flow.
- `Close` завершает сервер и всё дерево потомков.
- Новый runtime не создаёт `.stepan/`, manifest, raw log или state artifacts.

**Чек-лист реализации:**

- [ ] Переиспользовать существующие executable resolution и version parsing,
  выделив только реально общий небольшой helper при необходимости.
- [ ] Добавить process type с stdin/stdout/stderr pipes и явным `Start`/`Close`.
- [ ] Запускать argv для стабильного App Server contract без shell.
- [ ] Создать и назначить Job Object до возврата успешного `Start`.
- [ ] Конкурентно дренировать stderr в ограниченный in-memory diagnostic buffer,
  чтобы сервер не блокировался на pipe.
- [ ] Закрыть все pipes, process и job на каждом частичном error path.
- [ ] Добавить fake modes для неверной версии, раннего exit и child→grandchild.
- [ ] Проверить отсутствие потомков после normal close и failed startup.
- [ ] Не менять probe lifecycle сверх необходимого общего кода.

**Проверка:** `go test ./internal/codexapp` и `go test ./...`.

### I1-05. Долгоживущее JSON-RPC соединение

**Результат:** поверх существующего transport работает одно соединение с одним
handshake и несколькими последовательными client requests.

**Граница commit:** connection/dispatcher в `internal/codexapp`, fake protocol
сценарии и тесты.

**Критерии приёмки:**

- `initialize`/`initialized` выполняются ровно один раз на процесс.
- Responses сопоставляются исходным request ID; orphan и duplicate response
  завершают соединение fail-closed.
- Server notifications читаются до закрытия процесса.
- Server approval requests не блокируют чтение stdout.
- Одновременно допускается только явно поддержанный набор операций; неизвестный
  server request отклоняется, а не игнорируется.
- Закрытие соединения разблокирует все ожидающие операции одной понятной
  ошибкой.
- Существующий однопроходный probe продолжает проходить свои replay-тесты.

**Чек-лист реализации:**

- [ ] Переиспользовать `Transport`, `Decoder`, `Writer` и opaque typed IDs.
- [ ] Отделить reusable request dispatch от probe-specific stage machine,
  оставив probe поведение неизменным.
- [ ] Назначать client request IDs в одном месте.
- [ ] Запустить ровно один reader loop и сериализовать writes существующим
  writer.
- [ ] Передавать notifications и server requests зарегистрированному обработчику.
- [ ] На EOF/process exit завершать pending calls и запрещать дальнейшие calls.
- [ ] Добавить fake-тест: один handshake, несколько последовательных requests.
- [ ] Добавить fake-тесты orphan/duplicate response, unknown request и exit при
  ожидающем response.
- [ ] Прогнать существующие sanitized replay fixtures.

**Проверка:** `go test ./internal/codexapp` и `go test ./...`.

### I1-06. Thread/turn API и structured final output

**Результат:** долгоживущий client создаёт несколько threads и выполняет
несколько turns одного thread с отдельной JSON Schema на каждый turn.

**Граница commit:** thread/turn API и тесты в `internal/codexapp`.

**Критерии приёмки:**

- Новый flow создаёт новый thread, а последующие сообщения используют его ID.
- В одном процессе последовательно работают несколько threads и turns.
- Каждый `turn/start` получает canonical Git root как `cwd`,
  `approvalPolicy="on-request"`, отключённую сеть и переданную output schema.
- Final structured output берётся только из protocol field соответствующего
  terminal item, не извлекается из текста.
- Turn завершается только после согласованных item/turn terminal events.
- Невалидный JSON, отсутствующий final output, неверные correlation IDs и
  противоречивые terminal events завершают turn ошибкой без retry.
- В один момент на connection выполняется не более одного turn.

**Чек-лист реализации:**

- [ ] Добавить `StartThread` и минимальный thread handle без resume API.
- [ ] Добавить `RunTurn` с prompt, schema и per-turn options.
- [ ] Хранить активные thread/turn/item IDs только в памяти.
- [ ] Проверять correlation каждого события и approval request.
- [ ] Дождаться `turn/completed` и вернуть raw structured object вызывающему
  коду для предметного decode.
- [ ] Запретить concurrent turns простой явной ошибкой или mutex, без scheduler.
- [ ] Добавить fake-сценарий: thread A с двумя turns, затем thread B в том же
  процессе.
- [ ] Добавить table tests всех malformed/contradictory terminal вариантов.
- [ ] Подтвердить, что thread ID не сохраняется на диск.

**Проверка:** `go test ./internal/codexapp` и `go test ./...`.

### I1-07. In-memory per-turn approval policy

**Результат:** каждый turn получает ровно свои read/write permissions; file
change принимается автоматически только внутри каталога текущей спецификации.

**Граница commit:** approval evaluation и turn options в `internal/codexapp`,
тесты; durable probe policy остаётся рабочей.

**Критерии приёмки:**

- Read-only turn имеет пустой write allowlist, отключённую сеть и отклоняет любой
  запрос записи.
- Write-turn имеет ровно один writable root и принимает file-change approval
  только для наблюдённых путей внутри него.
- Approval одноразовый, связан с текущими thread/turn/item IDs и исчезает после
  turn.
- Command approvals, network, grant-root, permission amendment, broad и
  session-wide grants автоматически отклоняются.
- Запрос с отсутствующими либо чужими correlation IDs завершает turn fail-closed.
- Интерактивное подтверждение permission не запрашивается у пользователя.
- Новая политика не создаёт journal или state files.

**Чек-лист реализации:**

- [ ] Отделить чистую нормализацию/evaluation существующей `AccessPolicy` от
  probe-specific durable journal; не писать вторую реализацию path checks.
- [ ] Добавить read-only и single-write-root constructors для turn policy.
- [ ] Передавать соответствующий sandbox policy в `turn/start`.
- [ ] Наблюдать file-change items до обработки approval request.
- [ ] Отвечать на разрешённый file-change request только turn-scoped accept.
- [ ] Отклонять запрос расширения root или срока действия grant.
- [ ] Очищать все pending approvals при terminal event, interrupt и connection
  loss.
- [ ] Добавить тесты внутреннего пути, sibling-prefix обхода, внешнего пути,
  symlink/reparse обхода и session grant.
- [ ] Проверить, что в temporary repository не появились `.stepan` и artifacts.

**Проверка:** `go test ./internal/codexapp` и `go test ./...`.

### I1-08. Interrupt, shutdown и повторный запуск после сбоя

**Результат:** runtime безопасно завершает активный turn и App Server, а после
аварии позволяет создать новый процесс без resume старого flow.

**Граница commit:** lifecycle/cancellation в `internal/codexapp` и Windows fake
process-tree tests.

**Критерии приёмки:**

- Во время turn interrupt best-effort отправляет `turn/interrupt`, ждёт не более
  трёх секунд и затем закрывает Job Object.
- Вне turn shutdown закрывает Job Object сразу.
- После возврата shutdown App Server и потомки отсутствуют.
- Ранний exit сервера завершает текущий turn понятной ошибкой.
- После crash старый client не переиспользуется; новый `Start` создаёт новый
  процесс и новый thread.
- Ни interrupt, ни crash не создают retry/resume автоматически.
- Повторные interrupt/close безопасны и не зависают.

**Чек-лист реализации:**

- [ ] Хранить текущий turn ID только пока turn активен.
- [ ] Добавить best-effort `turn/interrupt` через то же connection.
- [ ] Ограничить ожидание terminal response тремя секундами timer/context.
- [ ] После grace period всегда закрывать job, pipes и pending calls.
- [ ] Отличать graceful close, operator interrupt и unexpected process exit в
  возвращаемой ошибке.
- [ ] Добавить fake server, который принимает interrupt, и server, который его
  игнорирует.
- [ ] Добавить Windows child→grandchild assertion для обоих сценариев.
- [ ] Добавить тест crash → новый process/thread, исключив reuse старого ID.
- [ ] Не добавлять автоматический timeout обычного turn.

**Проверка:** `go test ./internal/codexapp` и `go test ./...`.

### I1-09. Prompt contracts и строгие схемы результатов

**Результат:** `internal/specflow` содержит фиксированные prompts, допустимые
status enums и минимальные JSON Schemas для пяти типов turn.

**Граница commit:** contracts в `internal/specflow` и unit tests.

**Критерии приёмки:**

- Initial clarification допускает только `NEEDS_INPUT` или `READY_TO_WRITE`.
- Create write-turn допускает только `WRITTEN`; question — только `ANSWERED`;
  change analysis — `NEEDS_INPUT` или `READY_TO_UPDATE`; update — только
  `UPDATED`.
- `message` и `spec_id` обязательны только в предусмотренных состояниях и
  запрещены во всех остальных.
- Unknown fields, unknown status, пустой вопрос/ответ и пустой `spec_id`
  отклоняются.
- Пользовательский brief/question/change и абсолютный allowed path явно
  отделены в prompt и не меняют фиксированные инструкции.
- Тексты prompts содержат требования перечитать репозиторий/спецификацию и не
  писать во время read-only turns.

**Чек-лист реализации:**

- [ ] Определить один предметный result type и строгий decoder с
  `DisallowUnknownFields`.
- [ ] Создать минимальную schema для каждого turn, не одну широкую schema на все
  случаи.
- [ ] Перенести тексты из раздела 5.5 спецификации без добавления собственного
  шаблона specification.md.
- [ ] Формировать prompts маленькими функциями только там, где подставляются
  пользовательские данные или путь.
- [ ] Проверять ровно один вопрос для `NEEDS_INPUT` как непустое `message`; не
  пытаться лингвистически считать вопросительные предложения.
- [ ] Повторно прогонять `ValidateSpecID` для `READY_TO_WRITE`.
- [ ] Добавить table tests status/field combinations и unknown fields.
- [ ] Добавить точечные tests обязательных фрагментов prompt contracts.

**Проверка:** `go test ./internal/specflow` и `go test ./...`.

### I1-10. Первоначальное уточнение идеи

**Результат:** конкретный in-memory controller запускает idea flow, показывает
по одному вопросу и до `READY_TO_WRITE` не выполняет write-turn.

**Граница commit:** controller/state tests в `internal/specflow`.

**Критерии приёмки:**

- Непустой brief сразу запускает initial turn.
- Пустая команда переводит controller в состояние ожидания brief; ровно
  следующее пользовательское сообщение используется как brief.
- Каждый `NEEDS_INPUT` возвращает один вопрос, а ответ запускает отдельный turn
  того же thread.
- `READY_TO_WRITE` завершает только уточнение и не создаёт каталог или файл.
- Невалидный structured output завершает flow ошибкой без retry.
- Новый flow всегда получает новый thread; старый flow не возобновляется.
- Controller не знает о terminal rendering и тестируется fake turn runner.

**Чек-лист реализации:**

- [ ] Определить только фактически нужные состояния initial flow.
- [ ] Передать Git root как workspace при старте thread.
- [ ] Сохранить brief, thread ID и текущий вопрос только в памяти controller.
- [ ] На initial turn использовать read-only policy и initial schema.
- [ ] На ответ пользователя строить follow-up prompt того же thread с теми же
  ограничениями «один материальный вопрос» и «без записи».
- [ ] Не создавать общий workflow/event framework.
- [ ] Добавить fake runner script: question → answer → ready.
- [ ] Проверить отсутствие `docs/specs/<id>` после каждого read-only transition.
- [ ] Добавить tests invalid status, process error и повторного начала flow.

**Проверка:** `go test ./internal/specflow` и `go test ./...`.

### I1-11. Атомарный сценарий создания первого черновика

**Результат:** после `READY_TO_WRITE` controller безопасно разрешает один
write-turn и принимает только корректно записанный черновик внутри новой цели.

**Граница commit:** create-draft path в `internal/specflow`, нужная композиция с
`gitsnapshot`/`codexapp`, integration tests с fake runner.

**Критерии приёмки:**

- До write-turn валидируются `spec-id`, отсутствие цели и canonical containment.
- Baseline снимается непосредственно перед выдачей write permission.
- Write-turn получает ровно целевой каталог и create schema.
- Успех требует одновременно `WRITTEN`, существующий обычный
  `specification.md`, повторный path containment check и отсутствие новых
  изменений вне цели.
- При существующей цели write-turn не запускается, каталог не меняется.
- При boundary violation, невалидном status или отсутствии entrypoint flow
  завершается ошибкой и не удаляет partial files.
- Пользователю возвращается относительный путь к `specification.md`, а не полный
  Markdown.

**Чек-лист реализации:**

- [ ] Повторно вызвать pre-write target validation сразу после
  `READY_TO_WRITE`.
- [ ] Снять `gitsnapshot.Capture` перед выдачей turn policy.
- [ ] Запустить отдельный create write-turn с точным absolute target path.
- [ ] После turn повторно канонизировать target и проверить reparse containment.
- [ ] Проверить, что `specification.md` существует и является обычным файлом
  внутри цели.
- [ ] Снять post snapshot и сравнить его с baseline через I1-02.
- [ ] Сначала собрать все postconditions, затем перевести controller в draft
  menu state.
- [ ] В ошибке boundary перечислить внешние paths и указать, что cleanup не
  выполнялся.
- [ ] Добавить integration tests happy path, dirty baseline, existing target,
  missing entrypoint, outside write и partial write.

**Проверка:** `go test ./internal/specflow ./internal/gitsnapshot` и
`go test ./...`.

### I1-12. Вопрос по черновику и явное утверждение

**Результат:** пользователь может задать read-only вопрос либо завершить flow
только явной командой `/approve`.

**Граница commit:** question/approval transitions и tests в
`internal/specflow`.

**Критерии приёмки:**

- Question запускает отдельный read-only turn того же thread.
- Prompt требует заново прочитать текущие файлы с диска.
- При `ANSWERED` пользователю показывается `message`, после чего снова доступно
  draft menu.
- Question path не получает write permission.
- `/approve` проверяет только текущее наличие обычного `specification.md`, не
  сравнивает его с предыдущей версией и не запускает Codex.
- Успешный approval очищает in-memory flow/thread reference и возвращает main
  state, не создавая marker, state file или commit.
- Отсутствие `/approve` никогда не считается approval.

**Чек-лист реализации:**

- [ ] Добавить action для question с отдельным text input value.
- [ ] Использовать question prompt/schema и read-only turn policy.
- [ ] Передать answer message наружу, не записывая его в repository.
- [ ] После ответа вернуть draft link и доступные действия.
- [ ] Добавить approve transition с повторной проверкой entrypoint на диске.
- [ ] Не валидировать шаблон/разделы specification.md.
- [ ] Освободить thread только на стороне controller; App Server не закрывать.
- [ ] Добавить tests ручного изменения файла перед question и перед approval.
- [ ] Добавить tests question write request rejected и approve при удалённом
  entrypoint.

**Проверка:** `go test ./internal/specflow` и `go test ./...`.

### I1-13. Уточнение и применение изменения спецификации

**Результат:** change request сначала анализируется без записи, при
неоднозначности уточняется по одному вопросу и только затем получает отдельный
write-turn.

**Граница commit:** change/update transitions и integration tests в
`internal/specflow`.

**Критерии приёмки:**

- Первый change turn всегда read-only и требует перечитать текущие файлы.
- `NEEDS_INPUT` показывает ровно один вопрос; каждый ответ — новый read-only
  turn того же thread.
- До `READY_TO_UPDATE` write permission отсутствует и snapshot не считается
  разрешением на запись.
- После `READY_TO_UPDATE` снимается свежий baseline и запускается отдельный
  write-turn с одним writable root.
- Успех требует `UPDATED`, существующий `specification.md`, повторный containment
  check и отсутствие новых внешних изменений.
- После успеха снова показывается путь и draft menu.
- При ошибке partial files сохраняются, flow завершается и cleanup не
  выполняется.

**Чек-лист реализации:**

- [ ] Добавить change-analysis prompt/schema и read-only policy.
- [ ] Перед каждым analysis turn заново использовать текущий target path, не
  кэшированный текст файлов.
- [ ] Реализовать цикл `NEEDS_INPUT → answer → analysis` без общей loop engine.
- [ ] На `READY_TO_UPDATE` снять новый snapshot непосредственно перед write-turn.
- [ ] Запустить update prompt/schema с single-write-root policy.
- [ ] Переиспользовать общий post-write validator из create path; не копировать
  boundary logic.
- [ ] Вернуть controller в draft menu только после всех postconditions.
- [ ] Добавить tests direct update и clarification→update.
- [ ] Добавить tests ручной правки между menu и analysis, внешней записи,
  missing entrypoint и invalid `UPDATED` envelope.

**Проверка:** `go test ./internal/specflow ./internal/gitsnapshot` и
`go test ./...`.

### I1-14. Интерактивные формы и команды

**Результат:** `huh/v2`-интерфейс отображает main prompt, brief input и draft
menu, оставаясь тонким адаптером над протестированным controller.

**Граница commit:** UI adapter в `internal/specflow`, `go.mod`/`go.sum`, UI model
tests без terminal rendering.

**Критерии приёмки:**

- Main prompt не запускает flow автоматически.
- `/idea <literal rest>` передаёт весь непустой остаток строки как brief без
  shell quoting.
- `/idea` с пустым остатком открывает отдельный многострочный brief input.
- Неизвестная команда показывает короткую ошибку и возвращает main prompt.
- Draft menu явно содержит `/approve`, question и change.
- Question/change открывают отдельный text input.
- При установленном `ACCESSIBLE` формы используют встроенный accessible mode.
- `charm.land/huh/v2` — единственная новая прямая UI-зависимость.

**Чек-лист реализации:**

- [ ] Добавить pinned совместимую версию `charm.land/huh/v2`.
- [ ] Реализовать минимальный parser только для `/idea` и literal remainder.
- [ ] Не добавлять shell-like quotes, escapes, aliases или command registry.
- [ ] Связать формы с действиями controller, не перенося state transitions в UI.
- [ ] Печатать путь к entrypoint после create/update и после ответа на вопрос.
- [ ] Добавить accessible option по факту наличия `ACCESSIBLE`.
- [ ] Преобразовать cancel формы в единый sentinel для верхнего уровня.
- [ ] Unit-test parser и UI action model отдельно от `huh` rendering.
- [ ] Проверить `go mod tidy`, отсутствие второго TUI/CLI framework и
  минимальный diff зависимостей.

**Проверка:** `go test ./internal/specflow`, `go mod tidy`, `go test ./...`.

### I1-15. Executable, lazy lifecycle и `Ctrl+C`

**Результат:** `cmd/stepan` соединяет UI, controller и App Server runtime и
реализует полный process lifecycle интерактивной сессии.

**Граница commit:** `cmd/stepan`, минимальная wiring-функция в
`internal/specflow`, integration tests.

**Критерии приёмки:**

- До первой `/idea` процесс Codex не запускается.
- Stdin и stdout должны быть Windows console terminals; redirect/pipe
  завершается понятной ошибкой до запуска Codex.
- Первая `/idea` запускает один App Server; следующие flows переиспользуют его,
  пока процесс жив.
- `/approve` возвращает main prompt и не останавливает App Server.
- Crash App Server завершает текущий flow ошибкой и возвращает main prompt;
  следующая `/idea` запускает новый процесс/thread.
- `Ctrl+C` из любого UI state завершает Stepan без подтверждения.
- `Ctrl+C` во время turn использует interrupt grace period, вне turn закрывает
  Job Object сразу.
- Stepan не создаёт commit, `.stepan/` или durable state.

**Чек-лист реализации:**

- [ ] Добавить тонкий `main`/`run` с dependency construction и exit codes.
- [ ] Проверить `runtime.GOOS/GOARCH` и console handles до показа первой формы.
- [ ] Получить canonical Git root один раз при запуске приложения.
- [ ] Хранить nullable App Server runtime в session object и создавать его
  только при begin idea.
- [ ] Переиспользовать здоровый runtime для следующего flow.
- [ ] При server error закрыть старый runtime, сбросить flow и показать ошибку в
  main prompt.
- [ ] Связать `os.Interrupt`/form cancel с единым shutdown path.
- [ ] На выходе всегда закрывать runtime через `defer`, включая ошибки UI.
- [ ] Добавить fake integration: idle, два approved flows/один process,
  crash/restart и interrupt.
- [ ] Проверить, что Git `HEAD` и исходные файлы вне тестового spec root не
  изменились.

**Проверка:** `go test ./cmd/stepan ./internal/specflow ./internal/codexapp` и
`go test ./...`.

### I1-16. Сквозные fake-сценарии и регрессионный gate

**Результат:** один test-only commit фиксирует пользовательские критерии
итерации на собранном executable без запуска настоящего Codex.

**Граница commit:** только fake/integration test harness и tests; production
поведение не расширяется.

**Критерии приёмки:**

- Автоматизированы все проверяемые без настоящего terminal rendering критерии
  1–15 спецификации.
- Happy path проходит initial question, create, question, clarified update и
  explicit approval в одном thread.
- Второй `/idea` использует новый thread того же App Server.
- Отдельные сценарии покрывают existing target, invalid ID, missing entrypoint,
  external write, invalid envelope и App Server crash.
- Dirty baseline сохраняется; partial files после ошибок не удаляются.
- Interrupt test доказывает отсутствие fake child/grandchild.
- Полный suite не требует сети, установленного Codex или пользовательского
  профиля.

**Чек-лист реализации:**

- [ ] Расширить существующий fake App Server вместо добавления mock framework.
- [ ] Сделать сценарии детерминированными через argv/env test mode и уникальные
  temp paths.
- [ ] Проверять protocol requests: thread IDs, turn schemas, prompts, sandbox и
  approval responses.
- [ ] Проверять файловые postconditions независимо от ответа fake server.
- [ ] Проверять неизменность `HEAD`, index и заранее грязных файлов.
- [ ] Проверять отсутствие `.stepan`, approval marker, resume state и commit.
- [ ] Не пытаться snapshot-test внутреннюю отрисовку `huh`.
- [ ] Запустить suite несколько раз для выявления lifecycle race.
- [ ] Выполнить `go vet` и `git diff --check` как финальный gate.

**Проверка:** `go test ./...`, повторный `go test -count=10` для lifecycle
пакетов, `go vet ./...`, `git diff --check`.

## 4. Live-приёмка после commit-задач

Live dogfood не является implementation commit: его результатом намеренно
становится новая незафиксированная спецификация в рабочем дереве.

- [ ] Запустить собранный `cmd/stepan` из подкаталога текущего репозитория.
- [ ] Убедиться, что до `/idea` процесс App Server отсутствует.
- [ ] Выбрать реальную следующую небольшую доработку Stepan.
- [ ] Начать `/idea`, ответить минимум на один уточняющий вопрос и получить путь
  к первому черновику.
- [ ] Открыть файл обычными средствами и вручную изменить его перед следующим
  действием.
- [ ] Задать минимум один вопрос и убедиться, что Stepan показывает ответ и путь.
- [ ] Предложить минимум одно изменение; при вопросе Codex ответить и дождаться
  обновления файла.
- [ ] Завершить flow только через `/approve` и увидеть возврат в main prompt.
- [ ] Запустить второй `/idea` и убедиться, что создан новый thread без второго
  App Server process.
- [ ] Завершить `Ctrl+C` и убедиться, что App Server и потомки отсутствуют.
- [ ] Проверить `git status`: dogfood specification остаётся без commit,
  `.stepan/` и изменения вне её каталога отсутствуют.

## 5. Трассировка критериев спецификации

| Критерии спецификации | Основные задачи |
|---|---|
| 1–3 | I1-10, I1-14, I1-15 |
| 4–6 | I1-06, I1-10, I1-15 |
| 7–8 | I1-01, I1-02, I1-11 |
| 9–10 | I1-12, I1-13, I1-14 |
| 11 | I1-12, I1-14, I1-15 |
| 12 | I1-08, I1-15 |
| 13 | I1-11–I1-16 |
| 14–15 | I1-08, I1-15, I1-16 |

## 6. Definition of Done плана

- Все I1-01–I1-16 приняты последовательно; каждый commit самостоятельно
  собирается и проходит свои проверки.
- `go test ./...` проходит без сети и настоящего Codex.
- Pinned version, structured output, write boundary и Job Object работают
  fail-closed.
- Новый `cmd/stepan` реализует критерии спецификации без framework-кода «на
  будущее».
- Live dogfood выполнен, созданная им спецификация остаётся без commit.
- После завершения процесса не остаются App Server или его потомки.
- Не созданы `.stepan/`, durable approval state, resume, plan, implementation
  или Git commit от имени Stepan.
