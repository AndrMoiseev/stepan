# Stepan: specification flow intent → spec → plan

Статус: черновик

Основание: [intent](intent.md)

## 1. Назначение

Эта specification расширяет реализованный intent dialogue до durable feature
flow с фиксированными стадиями intent, specification и planning. Она определяет
пользовательское поведение, документы, agent roles, review/rework, prompt
composition, машинные контракты, recovery и phase commits.

Specification не описывает выполнение задач plan, проектные deterministic
checks и documentation stage после полной реализации feature.

## 2. Текущее поведение

Текущий `internal/specflow.Controller` управляет только intent dialogue.
Состояние и thread handle живут в памяти процесса; `/approve` завершает flow,
закрывает thread и возвращает UI в main prompt. Первый `intent.md` публикуется
сразу, последующие drafts проходят `apply | reject | rework`. Resume, durable
state, `spec.md`, `plan.md`, reviewer roles и phase commits отсутствуют.

Единственный embedded prompt `internal/specflow/prompts/bootstrap.md`
одновременно задаёт системный протокол и содержательные правила intent. Такая
форма не позволяет независимо заменять role/capability instructions, сохраняя
неизменяемый control-plane contract.

## 3. Целевой flow

```text
intent author dialogue
→ intent approval
→ intent phase commit
→ spec author dialogue
→ optional spec agent review/rework
→ spec approval
→ spec phase commit
→ plan author dialogue
→ optional plan agent review/rework
→ plan approval
→ plan phase commit
```

После plan commit feature flow остаётся активным с `current_stage: "plan"` и
`plan.status: "committed"`. Следующие стадии будут добавлены отдельно; до этого
такие features не отображаются в `/resume`.

## 4. Термины

Канонические определения feature flow находятся в корневом
[`CONTEXT.md`](../../../../CONTEXT.md). В частности, revision decision, agent
review и stage approval являются тремя разными пользовательскими операциями.

## 5. Требования

### Стадии и переходы

### REQ-001 — Фиксированный порядок стадий

Control plane должен разрешать только порядок `intent → spec → plan`. Нельзя
начать spec без committed intent и plan без committed spec.

### REQ-002 — Отдельная author-сессия каждой стадии

Каждая стадия использует собственную продолжительную author-сессию. Пока
thread доступен в текущем процессе, он переиспользуется; иначе Stepan создаёт
новый thread той же роли на основе durable-артефактов и `mem-log.md`.

### REQ-003 — Запрет возврата к intent

После перехода к spec возврат к intent как к обычной предыдущей стадии
запрещён. Существенное изменение intent закрывает текущую feature как
`superseded` и запускает новую feature.

### REQ-004 — Возврат от plan к spec

На стадии plan команда `/revise-spec` должна открыть spec author dialogue.
Approval spec снимается только после фактического изменения `spec.md`. Если
документ не изменился, `/approve` возвращает пользователя к plan без нового
commit.

После повторного approval изменённой spec предыдущий approval plan сохраняется
в истории, но plan получает `outdated: true` и должен быть пересмотрен и заново
утверждён.

### REQ-005 — Материальные решения принадлежат пользователю

Author и reviewer обязаны передавать пользователю любой выбор, способный
изменить intent, требования, cross-task архитектуру, acceptance criteria или
существенные ограничения. Agent может самостоятельно исправлять только
однозначные нарушения документного контракта.

### Документы и публикация

### REQ-006 — Канонические документы

Каталог feature должен использовать канонические имена `intent.md`, `spec.md`,
`plan.md`, `mem-log.md`, `state.json` и подкаталог `reviews/`.

### REQ-007 — Первый и последующие drafts

Первый валидный author draft стадии публикуется сразу. Каждый последующий draft
в обычном author dialogue сравнивается с опубликованной ревизией и ожидает
revision decision `apply | reject | rework`.

### REQ-008 — Pending draft не является durable-артефактом

Если пользователь завершает сеанс во время ожидания revision decision, pending
draft отбрасывается, внешний artifact root удаляется, а событие и показанный
diff остаются в `mem-log.md`. Resume возвращается к последней опубликованной
ревизии.

### REQ-009 — Открытые вопросы

Author по умолчанию не публикует draft с материальной неоднозначностью. Если
пользователь явно просит отложить конкретный вопрос, author записывает его в
обязательный раздел `Open questions` и может опубликовать draft.

Stage approval запрещён, пока раздел содержит любое содержимое. Маркер `None`
не используется. После явного решения пользователя вопрос удаляется, а решение
фиксируется в документе и `mem-log.md`. Решение «не делаем» является допустимым
явным решением.

### REQ-010 — Поддерживаемые ручные изменения

Пользователь может вручную изменять только опубликованные `intent.md`,
`spec.md` и `plan.md`. Ручное изменение `state.json`, `mem-log.md` или
`reviews/*.md` должно блокировать flow до восстановления согласованного
состояния.

После ручного изменения author обязан перечитать текущий документ. Фактическое
изменение spec на стадии plan возвращает flow к spec; изменение plan снимает
его текущий approval.

### REQ-011 — Классификация изменения утверждённого intent

При ручном изменении committed intent Stepan должен спросить пользователя,
существенно ли изменение.

- Несущественное изменение сохраняет approvals downstream-документов, заменяет
  approved hash intent и немедленно создаёт commit ревизии.
- Существенное изменение немедленно помечает старую feature как `superseded`,
  копирует изменённый intent в новую feature и ждёт его подтверждения.

Старая и новая features хранят двусторонние `supersedes`/`superseded_by` связи.
Закрытие старой и создание новой feature фиксируются одним commit до approval
нового intent. Если пользователь не подтвердит новую feature, старая всё равно
остаётся `superseded`.

### Agent review

### REQ-012 — Доступность agent review

Agent review отсутствует для intent и доступен по `/review` для опубликованных
spec и plan. До запуска он необязателен. После запуска stage approval
блокируется до согласованного завершения review: contract violations должны
быть исправлены, а каждое материальное finding должно получить решение
пользователя.

Отмена начатого review в первой версии не поддерживается.

### REQ-013 — Отдельная reviewer-сессия

Agent review выполняет отдельная сессия роли `spec-reviewer` или
`plan-reviewer`. Она переиспользуется, пока доступна; новая сессия читает
проверяемый документ, upstream-документы, актуальный review-файл, предыдущие
review-файлы и `mem-log.md`.

### REQ-014 — Review-артефакт

Reviewer пишет единственный `review.md` во внешний artifact root. Stepan
проверяет его, добавляет служебный YAML front matter и публикует как
`reviews/spec-N.md` или `reviews/plan-N.md`.

Один пользовательский запуск `/review` владеет одним numbered review-файлом.
Reviewer dialogue и внутренние recheck-итерации обновляют тот же файл и
публикуются сразу. Новый номер создаётся только новым пользовательским
запуском `/review` после изменения fingerprint.

### REQ-015 — Fingerprint review

Эквивалентность review определяется только hashes проверяемого документа и его
upstream-документов. Если такой fingerprint уже имеет completed review, Stepan
сообщает, что повторная проверка не нужна, и не запускает agent.

Provider, model, session и effective prompts в fingerprint не входят.

### REQ-016 — Изменение документов во время review

До запуска Stepan фиксирует target и upstream hashes. После завершения он
повторно читает все документы. При расхождении пользователь выбирает: повторить
review для текущих ревизий или считать результат применимым к текущему набору
hashes.

При принятии state и `mem-log.md` сохраняют первоначальные и принятые target и
upstream hashes. Последующая duplicate-проверка использует принятый fingerprint.

### REQ-017 — Полный набор findings

Каждый новый review-файл должен перечислять все известные findings данной
стадии с актуальными status. Одно и то же неустранённое замечание сохраняет ID.
Существенно изменённая проблема получает новый ID, а прежняя становится
`superseded`.

### REQ-018 — Контракт finding

Finding использует ID `SPEC-F-*` или `PLAN-F-*`, severity `blocker | major |
minor` и status `open | resolved | dismissed | superseded`. ID, severity,
исходное описание проблемы и location/traces неизменяемы.

Status, recommendation и resolution note могут обновляться. `superseded`
требует `Superseded-by`, `resolved` — `Resolution`, `dismissed` — rationale
пользователя в review-файле.

Если finding относится к отдельным элементам документа, он содержит `Traces`.
Для замечания ко всему документу поле `Traces` отсутствует.

### REQ-019 — Provenance решения finding

Review-файл должен хранить `Decision`, `Decided-by` и `Rationale`.

- Однозначное нарушение контракта получает `Decision: fix` и
  `Decided-by: reviewer`.
- Материальное finding до решения получает `Decision: pending` и
  `Decided-by: none`.
- Решение исправить получает `Decided-by: user`.
- Решение не исправлять переводит материальное finding в `dismissed` и
  сохраняет rationale пользователя.

Contract violation нельзя пометить `dismissed`.

### REQ-020 — Reviewer dialogue

Reviewer обсуждает с пользователем только материальные решения. Contract
violations фиксируются в review-файле и всегда подлежат исправлению. Пользователь
может принять рекомендации по всем pending findings командой `/apply` либо
уточнять решения свободным текстом.

После разрешения последнего материального вопроса Stepan явно сообщает о начале
rework и автоматически передаёт author-у только актуальный review-файл.

### REQ-021 — Автоматический rework и recheck

Author должен автоматически опубликовать редакцию, ограниченную согласованным
review scope. Пользователю показывается diff как уведомление, но дополнительный
revision decision не требуется. Тот же reviewer затем перечитывает документ и
подтверждает исправление каждого finding.

Если author обнаруживает необходимость нового материального решения, цикл
останавливается и вопрос передаётся пользователю. Новый материальный finding
reviewer-а также возвращает flow в reviewer dialogue; текущий retry counter при
этом сохраняется.

### REQ-022 — Ограничение автоматических попыток

Parser repair loop и reviewer → author → reviewer loop имеют общий default
лимит три последовательные автоматические попытки на один цикл. После
исчерпания Stepan показывает понятное сообщение и краткий отчёт о проблемах,
которые не удалось исправить, и переводит пользователя в author dialogue.

Новый review запускается только явной командой `/review` и получает новый лимит
три. После пользовательского вмешательства в parser error новый author draft
также начинает новый parser loop с лимитом три.

### Машинный контракт документов

### REQ-023 — Синтаксис стабильных ID

Parser должен распознавать ID в Markdown headings любого уровня:

```text
REQ-0*[1-9][0-9]*
DEC-0*[1-9][0-9]*
AC-0*[1-9][0-9]*
TASK-0*[1-9][0-9]*
SPEC-F-0*[1-9][0-9]*
PLAN-F-0*[1-9][0-9]*
```

Default documents используют padding до трёх цифр, но parser принимает любое
положительное количество цифр и leading zeros. Пропуски допустимы, `*-000`
запрещён.

Numeric suffix нормализуется как положительное целое без leading zeros:
`REQ-001`, `REQ-01` и `REQ-1` являются разными spellings одного канонического
ID `REQ-1`. Одновременное использование нескольких spellings одной identity
является duplicate ID. Default author сохраняет трёхзначное представление в
Markdown, а state и проверки повторного использования работают с канонической
identity.

### REQ-024 — Запрет повторного использования ID

ID считается использованным навсегда сразу после появления в agent draft,
включая впоследствии rejected или отброшенный pending draft. `state.json`
хранит все выданные IDs. Удалённый ID нельзя присвоить другому элементу или
повторно вернуть в документ.

### REQ-025 — Машинные поля ссылок

Поля `Traces:` и `Depends-on:` имеют фиксированные английские имена независимо
от языка документа.

- Каждый `AC-*` содержит `Traces` минимум на один существующий `REQ-*`.
- Каждый `TASK-*` содержит `Traces` минимум на один существующий `REQ-*` или
  `DEC-*`; ссылки на `AC-*` разрешены дополнительно.
- Каждый активный `REQ-*`, `DEC-*` и `AC-*` покрывается plan.
- Каждый `Depends-on` ссылается только на существующие `TASK-*`.
- Циклы task dependencies и ссылки на неизвестные или удалённые IDs запрещены.

### REQ-026 — Test scenarios plan

Каждый `TASK-*` содержит минимум один вложенный heading, начинающийся с
фиксированного `Test scenario`. Test scenario не имеет собственного ID,
содержит `Traces` минимум на один `AC-*` или `REQ-*`, а setup, действие и
ожидаемый результат описывает свободным текстом.

Каждый `AC-*` покрывается минимум одним test scenario. Parser проверяет наличие
и ссылки; семантическое соответствие проверяют plan author и reviewer.

Plan не хранит verification commands. Состав и запуск единых project-level
deterministic checks принадлежит Stepan и будет определён вместе с execution
flow; agent не влияет на их состав.

### REQ-027 — Default intent document

`intent/document` должен требовать проблему и контекст, ожидаемый наблюдаемый
результат, scope, exclusions, существенные ограничения и `Open questions`.
Intent не содержит REQ/DEC/AC, технический дизайн или задачи.

### REQ-028 — Default spec document

`spec/document` должен требовать ссылку на intent, текущее и целевое поведение,
`REQ-*`, `DEC-*`, применимые интерфейсы/данные/ошибки/миграции/безопасность и
совместимость, `AC-*`, способы проверки, exclusions и `Open questions`.

### REQ-029 — Default plan document

Каждый `TASK-*` в `plan/document` должен иметь один проверяемый outcome,
`Traces`, optional `Depends-on`, область и ожидаемые файлы, запрещённые области,
шаги реализации, task-local технические уточнения и test scenarios.

План не содержит `done when`, verification commands или documentation tasks.
Documentation выполняется отдельной стадией после реализации всей feature и в
эту specification не входит.

Task-local решение может уточнять способ реализации только одной задачи и не
противоречить spec. Любое cross-task, архитектурное или влияющее на acceptance
criteria решение требует `/revise-spec`.

### Prompts и agent contract

### REQ-030 — Роли

Control plane должен поддерживать роли `intent-author`, `spec-author`,
`spec-reviewer`, `plan-author`, `plan-reviewer`. Documentation roles пока не
добавляются.

### REQ-031 — Разделение prompt layers

Effective prompt состоит из immutable system contract, role prompt,
подключённых capabilities и runtime context. System contract задаёт протокол,
permissions, фиксированный artifact и машинные инварианты и всегда имеет
приоритет при конфликте.

Role prompts и capabilities являются содержательными и потенциально
заменяемыми. В первой версии используются только embedded defaults.

### REQ-032 — Логические prompt IDs

Prompt IDs равны нормализованным POSIX-путям без расширения, например
`system/spec-author`, `roles/spec-author` и
`capabilities/common/project-context`.

ID содержит только lowercase ASCII, использует `/`, не может быть абсолютным и
не содержит сегменты `.` или `..`. Фактическое разрешение effective fragment
может быть сложнее чтения одного файла.

Prompt IDs, sources и effective prompt hashes не сохраняются в `state.json`,
`mem-log.md` или review front matter.

### REQ-033 — Фиксированная композиция capabilities

Control plane, а не role prompt, задаёт ordered mapping ролей:

| Role | Capabilities |
|---|---|
| `intent-author` | `common/project-context`, `common/brainstorming`, `intent/author`, `intent/document` |
| `spec-author` | `common/project-context`, `common/brainstorming`, `spec/author`, `spec/document` |
| `spec-reviewer` | `common/project-context`, `spec/review`, `spec/document` |
| `plan-author` | `common/project-context`, `common/brainstorming`, `plan/author`, `plan/document` |
| `plan-reviewer` | `common/project-context`, `plan/review`, `plan/document` |

Полные IDs capabilities имеют префикс `capabilities/`. Будущая кастомизация
может изменять effective content зарегистрированного role/capability ID, но не
удалять capability из роли и не менять control-plane composition.

### REQ-034 — Общие capabilities

`common/project-context` требует читать релевантные project instructions,
документы и код, различать текущее и желаемое поведение, ссылаться на project
facts и явно сообщать о противоречиях.

`common/brainstorming` требует выявлять материальные неоднозначности, не решать
их за пользователя, показывать варианты и последствия, задавать один вопрос
или небольшой связанный набор и вести решения. Отложенный пользователем вопрос
переносится в `Open questions` и не запрещает публикацию draft, но блокирует
approval.

### REQ-035 — Специализированные capabilities

- `intent/author` ограничивает диалог границами intent.
- `spec/author` выводит полные требования, решения и acceptance criteria без
  task decomposition.
- `spec/review` проверяет соответствие intent, полноту, непротиворечивость,
  реализуемость, достаточность решений, тестируемость и document contract.
- `plan/author` строит зависимые проверяемые задачи без противоречий spec.
- `plan/review` проверяет покрытие spec, порядок, размер задач, объективную
  проверяемость и test scenarios.
- `*/document` задаёт соответствующий default document contract и доступен
  author и reviewer своей стадии.

### REQ-036 — Единый envelope

Все author/reviewer turns возвращают один из двух flat envelopes:

```json
{"kind":"message","message":"…","decisions":[]}
{"kind":"artifact","message":"","decisions":[]}
```

Все properties обязательны для transport compatibility. Пустой `message` у
`artifact` является transport-placeholder и нормализуется до доменного слоя.
Agent никогда не возвращает artifact path: system contract фиксирует
`intent.md`, `spec.md`, `plan.md` или `review.md` внутри artifact root.

### Durable state, журнал и resume

### REQ-037 — Durable state

Каждая feature хранит versioned `state.json` в своём каталоге и включает его в
Git. State отдельно моделирует `flow_status`, `current_stage`, status каждой
стадии и review status каждой стадии.

Минимальные stage statuses: `not_started | drafting | published | committed`.
Минимальные review statuses: `not_started | running | awaiting_decisions |
automatic_rework | escalated | completed`. Flow status текущего scope:
`active | superseded`.

### REQ-038 — Содержимое state

State должен хранить current/approved document hashes, upstream hashes,
`outdated`, все выданные IDs, retry counters, supersession links и metadata
review-запусков: ID, path, status, первоначальный/принятый fingerprint и число
попыток.

State не хранит Git commit SHA, provider thread handles или prompt metadata.
Provider, model, timestamps и review fingerprints находятся в review front
matter; provider/model не дублируются в state.

### REQ-039 — Review front matter

Stepan должен добавлять review-файлу YAML front matter как минимум с полями:

```yaml
---
review_id: SPEC-REVIEW-001
stage: spec
status: completed
created_at: 2026-08-30T12:00:00+03:00
updated_at: 2026-08-30T12:10:00+03:00
provider: codex
model: example-model
started_revision: <hash>
accepted_for_revision: <hash>
upstream_started:
  intent.md: <hash>
upstream_accepted:
  intent.md: <hash>
attempts: 2
---
```

Отдельный agent session ID и prompt metadata не сохраняются.

### REQ-040 — Append-only mem-log

`mem-log.md` остаётся человекочитаемым append-only журналом brief,
пользовательских и agent сообщений, решений, revision decisions, review
событий, hashes, автоматических попыток, ошибок, approvals и commits.

Каждая запись размечается stage, role и event kind. Новая сессия может читать
весь журнал, но текущий документ и актуальный review-файл являются
авторитетными артефактами, а не свободная переписка.

### REQ-041 — Resume

Команда `/resume` показывает незавершённые и доступные для продолжения flows
таблицей `№ | Feature | Current stage | Stage status | Review status | Updated`.
Пользователь выбирает flow номером.

Superseded features и active features с committed plan до появления
implementation stage в список не входят. Одновременно может существовать
несколько active flows, но только одна feature может иметь uncommitted changes;
resume другой feature в этом случае блокируется с объяснением.

### REQ-042 — Завершение пользовательского сеанса

`/exit`, EOF и Ctrl+C закрывают живые agent sessions, удаляют внешние artifact
roots, отбрасывают pending draft, сохраняют feature и последнее устойчивое
состояние. Они не записывают `flow canceled`. Отдельная команда отмены flow в
первой версии отсутствует.

### REQ-043 — Recovery agent operation

Если процесс прерван в `running` review или automatic rework, resume
возвращается к последнему устойчивому состоянию. Незавершённый agent turn не
продолжается автоматически; пользователь явно запускает новый `/review`.

### Git и phase commits

### REQ-044 — Чистое дерево при старте

Новая feature может начаться только при полностью чистом Git working tree и
index. Stepan не должен включать пользовательские или чужие feature changes в
свои commits.

### REQ-045 — Phase commit

`/approve` выполняет document/review/open-question validation, проверяет
чистоту всех путей вне текущей feature и затем создаёт commit только накопленных
файлов текущей feature. При любом preflight violation approval не фиксируется;
пользователь видит все причины и после исправления повторяет `/approve`.

При фактической ошибке `git commit` внутреннее состояние возвращается к
`published`, `mem-log.md` сохраняет неуспешную попытку, а пользователь снова
вводит `/approve`.

### REQ-046 — Commit messages

Phase и revision commits используют сообщения:

```text
feature(<feature-id>): approve intent
feature(<feature-id>): approve spec
feature(<feature-id>): approve plan
feature(<feature-id>): revise intent
feature(<old-id>): supersede with <new-id>
```

Supersession commit является явным исключением: он атомарно включает каталоги
старой и новой features. Stage не хранит связь с конкретным Git commit.

### REQ-047 — Recovery phase commit

Дополнительный `committing` status не используется. Перед Git commit state
получает итоговый `committed`. После аварии `FeatureRepository` сравнивает
feature-каталог с HEAD: чистый каталог означает состоявшийся commit, dirty —
незавершённую операцию и возврат stage в `published`.

Если известную частичную durable-операцию можно однозначно завершить по hashes,
`FeatureRepository` восстанавливает её автоматически. Иначе он ничего не
перезаписывает и блокирует flow с диагностикой.

### UI

### REQ-048 — Контекстные подсказки

Перед каждым пользовательским prompt UI показывает таблицу только допустимых
в текущем контексте команд с расшифровкой. Отдельная строка сообщает, когда
можно продолжить обычным текстом.

### REQ-049 — Команды planning flow

Контекстно доступны:

| Command | Meaning |
|---|---|
| `/review` | Запустить agent review текущей spec или plan |
| `/apply` | Принять рекомендации по всем pending material findings |
| `/approve` | Валидировать, утвердить и создать phase commit |
| `/revise-spec` | Вернуться от plan к spec |
| `/status` | Показать состояние flow, stages, documents и review |
| `/exit` | Завершить пользовательский сеанс с сохранением flow |

Revision decision `apply | reject | rework` показывается отдельной контекстной
таблицей и не является глобальным набором команд.

Main prompt показывает `/feature` для нового flow и `/resume` для выбора
доступного незавершённого flow.

## 6. Архитектурные и технические решения

### DEC-001 — Единственный state owner

`FeatureController` остаётся единственным модулем, изменяющим состояние feature
flow. UI и infrastructure adapters не воспроизводят правила переходов.

### DEC-002 — Общий StageEngine

Author/review lifecycle реализуется одним data-driven `StageEngine` с
фиксированными определениями intent, spec и plan. Различия стадий задаются
политикой, а не тремя копиями state machine.

### DEC-003 — Progress содержит интерфейс UI

`Progress` возвращает готовые `CommandHint {Command, Description}`. UI только
рисует таблицу и отправляет выбранную команду обратно controller-у.

### DEC-004 — Глубокий FeatureRepository

Durable поведение скрывается за interface доменных операций вроде публикации
draft, записи review, approval, supersession и recovery. Controller не
координирует отдельно state, journal, документы и Git, иначе порядок записи и
crash consistency размазываются по callers.

### DEC-005 — PromptCatalog как seam

`PromptCatalog` вводится сразу с тривиальным embedded adapter. Его interface
разрешает logical prompt IDs и собирает effective prompt роли; control plane не
зависит от физических файлов и будущей схемы customization.

### DEC-006 — Независимые состояния

Flow status, stage status и review status являются независимыми полями. Это
позволяет одновременно выразить committed intent, published spec и активный
spec review без комбинаторного плоского enum.

### DEC-007 — Stepan владеет project artifacts

Agent workspace read-only; agent пишет только фиксированный файл во внешний
artifact root. Stepan валидирует bytes, добавляет metadata, атомарно публикует
project-owned файлы, ведёт state/journal и выполняет Git operations.

### DEC-008 — Review как живой отчёт одного запуска

Один `/review` создаёт один numbered report, который обновляется reviewer
dialogue и recheck-loop. Git и append-only journal сохраняют историю изменений;
отдельные архивные версии основного документа или каждого recheck не создаются.

### DEC-009 — Parser перед agent reviewer

Детерминированные нарушения ID, ссылок, обязательных machine fields и структуры
возвращаются author-у без создания review-файла. Parser repair имеет тот же
лимит три, после чего проблема делегируется пользователю.

### DEC-010 — Provider-neutral envelope

Domain envelope `message | artifact` и его нормализация принадлежат
`specflow`. Codex получает flat schema без union keywords и со всеми required
properties; Claude adapter может использовать более широкую immutable schema,
но обязан локально подтвердить тот же domain contract.

### DEC-011 — Prompt system contract неизменяем

System prompt каждой role разрешается только из immutable embedded namespace.
Role/capability namespaces подготовлены к будущему effective resolution, но в
первой версии также используют embedded defaults. Конфликт всегда разрешается
в пользу system contract.

### DEC-012 — State не индексирует Git commits

`state.json` не хранит commit SHA или operation ID. История и сообщения Git
достаточны для аудита, а recovery текущей операции опирается на state, hashes и
состояние feature-каталога относительно HEAD.

## 7. Инварианты

- Никакой agent text не меняет state без валидного envelope и валидного
  artifact, когда он требуется.
- Пользователь принимает все материальные решения и любой stage approval.
- Agent review необязателен до запуска и обязателен к согласованному завершению
  после запуска.
- Contract violations нельзя dismiss и нельзя пронести через approval.
- Каждый автоматически применённый rework ограничен уже согласованным scope,
  видим пользователю и повторно проверяется reviewer-ом.
- Published document, state и review-файл важнее свободной истории диалога.
- ID никогда не переиспользуется внутри feature после первого появления.
- Project-owned служебные artifacts пишет только Stepan.
- Phase commit не включает пути вне текущей feature, кроме явного supersession
  commit двух связанных feature-каталогов.
- Provider choice не меняет semantic flow.

## 8. Ошибки и восстановление

- Invalid envelope, invalid artifact или parser error возвращается той же
  author/reviewer role в пределах автоматического лимита, а не немедленно
  уничтожает весь flow.
- Исчерпание лимита сохраняет последнее устойчивое состояние и передаёт
  понятный отчёт пользователю.
- Runtime crash закрывает runtime; durable feature не удаляется.
- Hash mismatch pending draft запрещает применение bytes, отличных от
  показанного diff.
- Неожиданное изменение служебного artifact блокирует flow без автоматической
  перезаписи пользовательского working tree.
- Неоднозначная частичная durable-операция блокирует resume с диагностикой.

## 9. Exclusions

- scheduler и execution lifecycle задач plan;
- реализация project-level deterministic checks;
- independent verification кода;
- documentation author/reviewer stage;
- автоматические task commits;
- пользовательские prompt override files и их precedence;
- сохранение prompt provenance;
- cancellation flow или review;
- автоматический resume незавершённого agent turn;
- параллельные stages или agent turns;
- архивирование каждой редакции основных документов.

## 10. Критерии приёмки

### AC-001 — Порядок стадий

Traces: REQ-001, REQ-002

После intent approval и успешного feature-only commit Stepan начинает отдельную
spec author-сессию; начать spec раньше невозможно.

### AC-002 — Specification и planning

Traces: REQ-004, REQ-006, REQ-027, REQ-028, REQ-029

Одна feature создаёт канонические `intent.md`, `spec.md` и `plan.md`; plan
начинается только после committed spec и использует отдельную author-сессию.

### AC-003 — Обычная публикация revision

Traces: REQ-007, REQ-008

Первый валидный draft публикуется сразу, последующий требует revision decision,
а `/exit` во время решения отбрасывает pending bytes и восстанавливает
published revision по durable history.

### AC-004 — Отложенные вопросы

Traces: REQ-009

По явной просьбе пользователя draft с `Open questions` публикуется, но
`/approve` возвращает понятную ошибку до явного решения и очистки раздела.

### AC-005 — Необязательный review

Traces: REQ-012, REQ-013, REQ-014

Spec и plan можно утвердить без запуска agent review. После `/review` отдельный
reviewer создаёт numbered report, и approval блокируется до завершения review.

### AC-006 — Reviewer dialogue и rework

Traces: REQ-019, REQ-020, REQ-021

Contract violations автоматически получают решение reviewer-а; материальные
findings обсуждаются с пользователем. После последнего решения author rework
запускается без второго `/apply`, diff показывается как уведомление, а reviewer
подтверждает результат.

### AC-007 — Retry и эскалация

Traces: REQ-022

Четвёртая последовательная автоматическая попытка не запускается. Пользователь
видит краткий отчёт, продолжает author dialogue и явно запускает новый review с
обнулённым лимитом.

### AC-008 — Duplicate и changed review

Traces: REQ-015, REQ-016

Одинаковый target/upstream fingerprint не запускается повторно. Изменение
любого hash во время review требует явного выбора пользователя; принятое
соответствие сохраняется и участвует в следующей duplicate-проверке.

### AC-009 — Findings сохраняют идентичность

Traces: REQ-017, REQ-018, REQ-019

Повторный review перечисляет старые findings с актуальными statuses, сохраняет
их ID и severity, требует rationale для dismissed и новую identity для
существенно изменившейся проблемы.

### AC-010 — Машинная валидация spec

Traces: REQ-023, REQ-024, REQ-025

Parser принимает headings любого уровня, отклоняет duplicate/reused/zero IDs,
alias-дубликаты вроде `REQ-001`/`REQ-1`, неизвестные ссылки и AC без ссылки на
REQ. ID из rejected draft остаются зарезервированными после resume.

### AC-011 — Машинная валидация plan

Traces: REQ-025, REQ-026, REQ-029

Parser отклоняет task без REQ/DEC trace, циклические dependencies, task без test
scenario и plan, в котором хотя бы один AC не покрыт test scenario.

### AC-012 — Prompt composition

Traces: REQ-030, REQ-031, REQ-032, REQ-033, REQ-034, REQ-035

Каждая роль получает immutable system contract и фиксированный ordered набор
embedded role/capability prompts по логическим IDs без расширения. UI/runtime
не выбирают composition.

### AC-013 — Provider-neutral envelope

Traces: REQ-036

Codex и Claude принимают `message | artifact` с одинаковой доменной семантикой;
пустой artifact message не попадает в доменный слой, а path отсутствует в
output.

### AC-014 — Resume с новой сессией

Traces: REQ-037, REQ-038, REQ-040, REQ-041, REQ-042, REQ-043

После `/exit` и нового процесса `/resume` показывает feature, создаёт новую
роль из документов и размеченного журнала и продолжает с последнего устойчивого
состояния без provider thread handle.

### AC-015 — Несколько flows

Traces: REQ-041, REQ-044, REQ-045

`/resume` показывает несколько чисто приостановленных flows, но блокирует
работу с другой feature, если одна feature имеет uncommitted changes.

### AC-016 — Phase approval и commit

Traces: REQ-044, REQ-045, REQ-046, REQ-047

Dirty path вне feature предотвращает сам approval. На чистом дереве approval
создаёт feature-only commit; commit failure возвращает stage в published, а
crash между state write и Git commit восстанавливается по HEAD и hashes.

### AC-017 — Возврат к spec

Traces: REQ-004

`/revise-spec` переиспользует живую spec session либо создаёт новую. Неизменная
spec возвращает plan без commit; изменённая требует нового approval/commit и
помечает прежний plan outdated.

### AC-018 — Изменение intent

Traces: REQ-003, REQ-011

Несущественная ручная revision intent сохраняет downstream approvals и сразу
коммитится. Существенная создаёт linked feature, немедленно supersede-ит старую
одним двухкаталожным commit и ждёт подтверждения нового intent.

### AC-019 — Контекстные команды

Traces: REQ-048, REQ-049

Каждый пользовательский prompt показывает таблицу только допустимых команд с
описаниями; `/resume` показывает согласованные столбцы и принимает номер.

### AC-020 — Служебные artifacts защищены

Traces: REQ-010, REQ-037, REQ-039, REQ-040

Ручное изменение state, journal или review обнаруживается и блокирует flow;
ручное изменение основного документа проходит определённый stage-specific
lifecycle.

### AC-021 — Module interfaces являются test surface

Traces: REQ-001, REQ-012, REQ-037, REQ-045

Happy paths, invalid transitions, review/retry, crash recovery и Git preflight
проверяются через interfaces `FeatureController`, `StageEngine`,
`FeatureRepository` и `PromptCatalog`, без assertions на их внутреннее состояние.

### AC-022 — Provider parity

Traces: REQ-002, REQ-013, REQ-036

Один provider-neutral conformance suite подтверждает одинаковый author,
reviewer, resume и artifact contract для Codex и Claude adapters.

### AC-023 — Пользователь сохраняет власть над решениями

Traces: REQ-005, REQ-019, REQ-020

Ни один material finding не запускает author rework до решения пользователя.
Reviewer самостоятельно принимает только однозначное исправление document
contract, а пользователь может отклонить любое содержательное замечание с
явным rationale.

## 11. Способы проверки

- table-driven domain tests state transitions всех стадий и review statuses;
- filesystem tests `FeatureRepository` с temp Git repositories, crash points и
  ручными изменениями artifacts;
- parser tests ID lifecycle, references, open questions и plan coverage;
- fake agentruntime scenarios author/reviewer dialogue, rework и retry;
- provider-neutral conformance tests Codex/Claude envelope и artifact roots;
- UI model tests контекстных command tables и `/resume` selection;
- Git integration tests feature-only commits, dirty-tree blocking,
  supersession и commit recovery;
- `go test ./internal/specflow`, затем `go test ./...`.

## Open questions
