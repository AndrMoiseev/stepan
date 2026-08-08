# Stepan: продуктовые решения и рабочие предположения

Статус: принятые решения обязательны для исходной редакции; рабочие предположения требуют проверки  
Дата: 2026-08-08

Архитектурные ADR могут конкретизировать эти решения, но не должны молча менять их. Изменение решения требует новой product requirement revision.

## Принятые решения

- Первый пользователь — один разработчик.
- Stepan является локальным CLI-инструментом.
- Product/spec source of truth хранится в Git. Эфемерный runtime state не становится вторым источником продуктовой истины.
- Первый и единственный обязательный delivery backend — локальный Git. GitHub, GitLab, PR и удалённый CI не требуются.
- Произвольные агенты подключаются через открытый adapter contract; ядро Stepan не привязано к конкретному CLI, API, модели или провайдеру.
- Языки программирования, build tools и test frameworks не фиксируются продуктом: проект объявляет проверки и команды через project profile.
- Каждая агентная роль запускается в sandbox; режим без sandbox не является поддерживаемым режимом исполнения.
- Stepan не выполняет deployment, публикацию, изменение production или иные мутации внешних продуктовых систем.
- Stepan подготавливает verified local candidate branch/commit, а локальный merge выполняет разработчик.
- Agent adapters запускаются отдельными процессами и обмениваются с ядром сообщениями по версионированному JSON Lines протоколу через stdin/stdout.
- Risk model содержит ровно два уровня: `normal` и `critical`.
- Stepan может предлагать patch авторитетной спецификации, но применяет его только после явного подтверждения разработчика.
- Agent adapters могут передавать scoped source code настроенным облачным model providers; отдельное подтверждение на каждый вызов не требуется.
- Project-persistent артефакты хранятся в `<repository-root>/.stepan/` и версионируются Git.
- Промежуточное crash-recoverable состояние активных run хранится в `~/.stepan/state/<repository-id>/` и не попадает в Git.
- Disposable sandbox/temp data хранится в `~/.stepan/cache/<repository-id>/`.
- В Git сохраняются компактные evidence manifests; полные stdout/stderr и transcripts остаются в пользовательском state directory.
- Task plan и dependency graph генерирует Stepan из governing specification; разработчик может выполнить ревью и потребовать изменения.
- Project artifacts используют Markdown для intent/spec/decisions, YAML для policy/role/task declarations и JSON для machine-produced verdict/evidence manifests.
- Конкретный sandbox backend выбирается при проработке архитектуры; требования фиксируют backend-neutral контракт и обязательные гарантии.
- Agent adapter executables устанавливаются глобально в `~/.stepan/adapters/`; проект хранит только adapter ID/version, configuration и integrity metadata.
- В P0 доступен только фиксированный каталог framework-owned flows. Настраиваемые project workflows остаются целевым расширением и не должны требовать переписывания Controller.
- Generated plan фиксируется Controller-ом отдельным Git commit, SHA которого становится governing plan revision.
- Нормативные продуктовые спецификации хранятся в `.stepan/spec/`.
- Adapter считается доверенным после явной локальной установки разработчиком, фиксации exact version и SHA-256 digest; обязательная цифровая подпись не требуется.
- При ревью generated plan разработчик может как править YAML напрямую, так и передавать комментарии Stepan для replan; оба пути создают новую plan revision.
- Core task lifecycle фиксирован и не расширяется workflow-ами; настраиваемыми остаются только внутренние stages и transitions attempt-а.
- P0 поставляет пять framework-owned flows: `plan`, `execute-task`, `recover`, `spec-change`, `status`.

## Рабочие предположения

- Stepan работает с одним локальным Git-репозиторием на run.
- Первый поддерживаемый сценарий — изменение существующего программного проекта, а не создание и эксплуатация production-инфраструктуры.
- P0 выполняет задачи последовательно, в отдельной ветке и worktree.
- Политики и спецификации хранятся в человекочитаемом, версионируемом формате в Git; формат durable execution records определяется ADR.

## Сводный реестр

| Вопрос | Решение |
|---|---|
| Первый пользователь | один разработчик |
| Форма продукта | локальный CLI |
| Product/spec source of truth | файлы в Git |
| Agent runtimes | произвольные агенты через adapters |
| Языки и build/test ecosystems | не фиксируются; задаются project profile |
| Delivery backend | только локальный Git |
| Исполнение агентов | обязательный sandbox |
| Внешние продуктовые side effects | запрещены |
| Локальный merge | выполняет разработчик |
| Adapter process model | отдельный процесс, JSON Lines через stdin/stdout |
| Risk model | два уровня: `normal`, `critical` |
| Изменение спецификации | Stepan предлагает patch, разработчик явно подтверждает |
| Cloud agents | передача scoped-кода внешнему provider-у разрешена |
| Project-persistent artifacts | Git-tracked `<repository-root>/.stepan/` |
| Intermediate run state | `~/.stepan/state/<repository-id>/` |
| Disposable cache/temp | `~/.stepan/cache/<repository-id>/` |
| Evidence persistence | compact manifests в Git; raw outputs/transcripts в user state |
| Task planning | plan генерирует Stepan; разработчик может выполнить review/replan |
| Project formats | Markdown для spec/decisions, YAML для policy/plan/tasks, JSON для verdict/evidence |
| Sandbox backend | конкретная технология откладывается до ADR; contract остаётся backend-neutral |
| Adapter installation | глобально в `~/.stepan/adapters/`, exact ID/version привязаны в project policy |
| Workflow catalog P0 | только фиксированные framework-owned flows с exact ID/version |
| Configurable workflows | отложены; граница contracts сохраняется для будущего расширения без переписывания Controller |
| Reference plan/run | отдельные `plan` и `run` на первом этапе |
| Generated plan persistence | отдельный Controller-owned Git commit и governing plan SHA |
| Plan review | прямой YAML patch и review comments/replan; всегда новая revision |
| Normative specification root | `.stepan/spec/` + validated `index.yaml` |
| Adapter trust | explicit install + exact version + SHA-256; подпись не обязательна |
| Core lifecycle | фиксированный enum `planned`, `ready`, `in_progress`, `merge_ready`, `done`, `blocked`, `failed` |
| P0 flows | `plan`, `execute-task`, `recover`, `spec-change`, `status` |

Открытых продуктовых вопросов для исходной редакции не осталось.
