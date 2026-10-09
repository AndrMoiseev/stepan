# Карта контекстов Stepan

Модуль на этой карте — один каталог с Go-пакетом в `cmd/` или `internal/`. Включены также пакеты тестовой и CI-инфраструктуры. Стрелка `A → B` означает, что `arch-go.yml` **разрешает** модулю `A` импортировать внутренний модуль `B`; она не утверждает, что импорт сейчас присутствует в исходниках. Внешние библиотеки на схеме не показаны.

Для модулей без правила `shouldOnlyDependsOn` конфигурация не задаёт перечень допустимых исходящих зависимостей. В таблице ниже такие модули отмечены как «Исходящие правила не заданы». Для модулей с запретом всех внутренних импортов указано «Нет: внутренние импорты запрещены».

## Разрешённые внутренние зависимости

![Обзор разрешённых зависимостей модулей](archify/module-dependencies.svg)

[Открыть интерактивную схему](archify/module_dependencies.html). В IntelliJ IDEA после перехода к HTML-файлу нажмите `Alt+F2`, чтобы открыть его в браузере. Схема показывает ключевые связи и группирует адаптеры агентов. `openspec` и вложенный `flows/impl_loop/checkexec` показаны отдельно. Полный перечень разрешённых целей приведён ниже; пути целей указаны относительно `internal/`.

| Модуль | Разрешённые внутренние цели по `arch-go.yml` |
|---|---|
| `cmd/stepan` | `agentruntime`, `agentruntime/claudeapp`, `agentruntime/codexapp`, `agentruntime/nessyapp`, `flows/spec`, `flows/impl_loop`, `flows/impl_loop/runtime`, `flows/impl_loop/store`, `platformsupport`, `setting` |
| `cmd/codex-appserver-probe` | `codexprobe` |
| `internal/flows/spec` | `agentruntime`, `git` |
| `internal/flows/impl_loop` | `agentruntime`, `git`, `processjob`, `openspec`, `flows/impl_loop/store`, `flows/impl_loop/checkexec`, `setting`, `flows/impl_loop/state` |
| `internal/agentruntime` | Нет: внутренние импорты запрещены |
| `internal/agentruntime/claudeapp` | `agentruntime` |
| `internal/agentruntime/codexapp` | `agentruntime`, `platformsupport`, `processjob` |
| `internal/agentruntime/nessyapp` | `setting`, `agentruntime`, `platformsupport`, `processjob` |
| `internal/flows/impl_loop/runtime` | Исходящие правила не заданы |
| `internal/setting` | Нет: внутренние импорты запрещены |
| `internal/flows/impl_loop/state` | Нет: внутренние импорты запрещены |
| `internal/openspec` | `git`, `processjob` |
| `internal/flows/impl_loop/checkexec` | `processjob` |
| `internal/flows/impl_loop/store` | `git`, `processjob`, `flows/impl_loop/state` |
| `internal/codexprobe` | `agentruntime/codexapp`, `git` |
| `internal/git` | Нет: внутренние импорты запрещены |
| `internal/platformsupport` | Нет: внутренние импорты запрещены |
| `internal/processjob` | Нет: внутренние импорты запрещены |
| `internal/agentruntime/conformance` | Исходящие правила не заданы |
| `internal/architecture` | Нет: внутренние импорты запрещены |
| `internal/testscope` | Исходящие правила не заданы |
| `internal/testscope/cmd` | Исходящие правила не заданы |

`internal/architecture` проверяет правила архитектуры, а не предоставляет production API. Для `internal/agentruntime`, `internal/setting`, `internal/flows/impl_loop/state`, `internal/architecture`, `internal/git`, `internal/platformsupport` и `internal/processjob` конфигурация явно запрещает импорты других внутренних пакетов. `flows/impl_loop/store` вправе импортировать вложенный `state` как модель сохраняемых данных; остальные пакеты flow для него закрыты.

## Модули

| Модуль | Назначение | Ответственность | Граница |
|---|---|---|---|
| `cmd/stepan` | Пользовательская точка входа. | CLI, терминальный интерфейс, preflight и сборка зависимостей для двух flow. | Передаёт выполнение `flows/spec` и `flows/impl_loop`; состояние их процессов принадлежит им. |
| `cmd/codex-appserver-probe` | Отдельная диагностическая программа. | Читает параметры probe и выводит результат проверки App Server. | Делегирует саму диагностику `internal/codexprobe`; не входит в обычный пользовательский flow. |
| `internal/flows/spec` | Flow подготовки intent, specification и plan. | Диалог, состояние feature, документы, review и команды пользователя. | Работает через контракт `agentruntime` и Git-снимки; реализация задач принадлежит `flows/impl_loop`. |
| `internal/flows/impl_loop` | Flow автономной реализации. | Выбор заданий, агентские вызовы, проверки, review, приёмка, коммиты и восстановление run. | Оркестрирует процесс; документы change и исполнение проверок выделены в соседние модули, хранение и переходы состояния — во вложенные `store` и `state`. |
| `internal/agentruntime` | Общий контракт агентского runtime. | Интерфейсы runtime и thread, конфигурация доступа, проверка структурированного ответа. | Не владеет транспортом конкретного провайдера. |
| `internal/agentruntime/claudeapp` | Адаптер Claude CLI. | Создание thread и вызовы через SDK, ограничения инструментов и доступа к файлам. | Реализует контракт `agentruntime`; правила прикладных flow остаются у потребителей. |
| `internal/agentruntime/codexapp` | Адаптер Codex App Server. | Жизненный цикл процесса, JSON-RPC, thread/turn, approvals и проверка доступа. | Реализует контракт `agentruntime`; состояние feature или implementation run не принадлежит адаптеру. |
| `internal/agentruntime/nessyapp` | Адаптер Nessy. | ACP-сеансы, отдельные процессы thread, авторизация и контроль доступа. | Реализует контракт `agentruntime`; токен получает через callback `setting`, дерево процессов контролирует `processjob`. |
| `internal/flows/impl_loop/runtime` | Сборка runtime для implementation flow. | Создаёт фабрики Codex, Claude и Nessy из профиля роли и параметров процесса. | Связывает адаптеры с `flows/impl_loop`; не управляет ходом реализации. Исходящие импорты не ограничены `arch-go.yml`. |
| `internal/setting` | Общий контракт настроек. | Читает два файла, объединяет профили и настройки flow, проверяет команды и rules file; отдельно выдаёт токен Nessy. | Возвращает эффективные значения без секрета; не запускает команды и не хранит состояние run. |
| `internal/flows/impl_loop/state` | Модель состояния implementation run. | Задачи, назначения, попытки, доказательства, статусы и допустимые переходы. | Не обращается к Git, хранилищу или агентам; их результаты передаются через операции модели. |
| `internal/openspec` | Вход документов OpenSpec change. | Читает полный набор документов и фиксирует версию входа. | Не разбирает задания и не управляет flow. |
| `internal/flows/impl_loop/checkexec` | Выполнение настроенных проверок implementation flow. | Запускает команды, ограничивает время, собирает вывод и классифицирует сбои. | Получает уже разрешённую команду; хранение логов и решение о приёмке принадлежат пакету flow. |
| `internal/flows/impl_loop/store` | Долговременное хранение implementation run. | Раскладка файлов run, неизменяемые артефакты, JSONL-журнал и SQLite-проекция. | Сохраняет и восстанавливает данные; решение о следующем шаге принимает `flows/impl_loop`. |
| `internal/codexprobe` | Диагностика Codex App Server. | Прогон probe, replay, управление ожидающими approval и Git-снимками кандидата. | Обслуживает отдельную диагностическую программу; пользовательский flow не строится на probe. |
| `internal/git` | Контроль состояния Git-репозитория. | Снимки дерева, index и submodules, сравнение изменений, проверка границы записи и точечное восстановление файлов. | Не выбирает политику flow; потребитель задаёт допустимую область изменений. |
| `internal/platformsupport` | Проверка поддерживаемой платформы. | Валидирует сочетание OS и архитектуры. | Не запускает процессы и не выбирает агентский провайдер. |
| `internal/processjob` | Управление дочерними процессами. | Удерживает и завершает дерево процессов средствами платформы. | Не знает протоколы Codex, Nessy или команды проверок. |
| `internal/agentruntime/conformance` | Общие контрактные проверки адаптеров. | Поставляет тестовые сценарии runtime и политик записи. | Используется тестами; исходящие зависимости не заданы `arch-go.yml`. |
| `internal/architecture` | Архитектурные тесты. | Проверяет `arch-go.yml` и build tags интеграционных тестов. | Содержит только тестовый код; внутренние импорты запрещены конфигурацией. |
| `internal/testscope` | Выбор интеграционных наборов для CI. | Классифицирует изменённые пути по Git и process suite. | Не запускает тесты; исходящие зависимости не заданы `arch-go.yml`. |
| `internal/testscope/cmd` | CLI для CI-классификатора. | Принимает событие и список файлов, печатает выбранные наборы. | Передаёт классификацию `internal/testscope`; исходящие зависимости не заданы `arch-go.yml`. |

Источник правил зависимостей: [`arch-go.yml`](../../../arch-go.yml). Назначение и границы сверены с текущими Go-пакетами; при изменении кода и правил эту карту нужно обновить.
