# Карта контекстов Stepan

Модуль на этой карте — один каталог с Go-пакетом в `cmd/` или `internal/`. Включены также пакеты тестовой и CI-инфраструктуры. Стрелка `A → B` означает, что `arch-go.yml` **разрешает** модулю `A` импортировать внутренний модуль `B`; она не утверждает, что импорт сейчас присутствует в исходниках. Внешние библиотеки на схеме не показаны.

Для модулей без правила `shouldOnlyDependsOn` конфигурация не задаёт перечень допустимых исходящих зависимостей. Такие узлы отмечены пунктирной рамкой и оставлены без исходящих стрелок. Узлы с правилом `shouldNotDependsOn` на все внутренние пакеты также не имеют исходящих стрелок, но их запрет определён явно.

## Разрешённые внутренние зависимости

```mermaid
flowchart LR
    subgraph Entry[Исполняемые пакеты]
        stepan[cmd/stepan]
        probe_cmd[cmd/codex-appserver-probe]
    end
    subgraph Flows[Прикладные процессы]
        spec[internal/flows/spec]
        impl[internal/flows/impl_loop]
    end
    subgraph Runtime[Агентские runtime и сборка]
        runtime[internal/agentruntime]
        claude[internal/agentruntime/claudeapp]
        codex[internal/agentruntime/codexapp]
        nessy[internal/agentruntime/nessyapp]
        runtime_factory[internal/implementationruntime]
    end
    subgraph Services[Состояние и операции реализации]
        config[internal/setting]
        state[internal/implementationstate]
        openspec[internal/openspec]
        checks[internal/checkexec]
        store[internal/runstore]
        probe[internal/codexprobe]
    end
    subgraph Infrastructure[Общая инфраструктура]
        git[internal/git]
        platform[internal/platformsupport]
        jobs[internal/processjob]
    end
    subgraph TestTools[Тесты и CI]
        conformance[internal/agentruntime/conformance]
        architecture[internal/architecture]
        testscope[internal/testscope]
        testscope_cmd[internal/testscope/cmd]
    end

    stepan --> runtime & claude & codex & nessy & impl & runtime_factory & platform & spec & store & config
    probe_cmd --> probe
    claude --> runtime
    codex --> runtime & platform & jobs
    nessy --> config & runtime & platform & jobs
    probe --> codex & git
    spec --> runtime & git
    impl --> runtime & git & jobs & openspec & store & checks & config & state
    openspec --> git & jobs
    checks --> git & jobs
    store --> git & jobs & state

    classDef unspecified stroke-dasharray: 5 5
    class runtime_factory,conformance,testscope,testscope_cmd unspecified
```

`internal/architecture` проверяет правила архитектуры, а не предоставляет production API. Для `internal/agentruntime`, `internal/setting`, `internal/implementationstate`, `internal/architecture`, `internal/git`, `internal/platformsupport` и `internal/processjob` конфигурация явно запрещает импорты других внутренних пакетов.

## Модули

| Модуль | Назначение | Ответственность | Граница |
|---|---|---|---|
| `cmd/stepan` | Пользовательская точка входа. | CLI, терминальный интерфейс, preflight и сборка зависимостей для двух flow. | Передаёт выполнение `flows/spec` и `flows/impl_loop`; состояние их процессов принадлежит им. |
| `cmd/codex-appserver-probe` | Отдельная диагностическая программа. | Читает параметры probe и выводит результат проверки App Server. | Делегирует саму диагностику `internal/codexprobe`; не входит в обычный пользовательский flow. |
| `internal/flows/spec` | Flow подготовки intent, specification и plan. | Диалог, состояние feature, документы, review и команды пользователя. | Работает через контракт `agentruntime` и Git-снимки; реализация задач принадлежит `flows/impl_loop`. |
| `internal/flows/impl_loop` | Flow автономной реализации. | Выбор заданий, агентские вызовы, проверки, review, приёмка, коммиты и восстановление run. | Оркестрирует процесс; документы change, исполнение проверок, долговременное хранение и переходы состояния выделены в соседние модули. |
| `internal/agentruntime` | Общий контракт агентского runtime. | Интерфейсы runtime и thread, конфигурация доступа, проверка структурированного ответа. | Не владеет транспортом конкретного провайдера. |
| `internal/agentruntime/claudeapp` | Адаптер Claude CLI. | Создание thread и вызовы через SDK, ограничения инструментов и доступа к файлам. | Реализует контракт `agentruntime`; правила прикладных flow остаются у потребителей. |
| `internal/agentruntime/codexapp` | Адаптер Codex App Server. | Жизненный цикл процесса, JSON-RPC, thread/turn, approvals и проверка доступа. | Реализует контракт `agentruntime`; состояние feature или implementation run не принадлежит адаптеру. |
| `internal/agentruntime/nessyapp` | Адаптер Nessy. | ACP-сеансы, отдельные процессы thread, авторизация и контроль доступа. | Реализует контракт `agentruntime`; токен получает через callback `setting`, дерево процессов контролирует `processjob`. |
| `internal/implementationruntime` | Сборка runtime для implementation flow. | Создаёт фабрики Codex, Claude и Nessy из профиля роли и параметров процесса. | Связывает адаптеры с `flows/impl_loop`; не управляет ходом реализации. Исходящие импорты не ограничены `arch-go.yml`. |
| `internal/setting` | Общий контракт настроек. | Читает два файла, объединяет профили и настройки flow, проверяет команды и rules file; отдельно выдаёт токен Nessy. | Возвращает эффективные значения без секрета; не запускает команды и не хранит состояние run. |
| `internal/implementationstate` | Модель состояния implementation run. | Задачи, назначения, попытки, доказательства, статусы и допустимые переходы. | Не обращается к Git, хранилищу или агентам; их результаты передаются через операции модели. |
| `internal/openspec` | Вход документов OpenSpec change. | Читает полный набор документов и фиксирует версию входа. | Не разбирает задания и не управляет flow. |
| `internal/checkexec` | Выполнение настроенных проверок. | Запускает команды, ограничивает время, собирает вывод и классифицирует сбои. | Получает уже разрешённую команду; хранение логов и решение о приёмке принадлежат другим модулям. |
| `internal/runstore` | Долговременное хранение implementation run. | Раскладка файлов run, неизменяемые артефакты, JSONL-журнал и SQLite-проекция. | Сохраняет и восстанавливает данные; решение о следующем шаге принимает `flows/impl_loop`. |
| `internal/codexprobe` | Диагностика Codex App Server. | Прогон probe, replay, управление ожидающими approval и Git-снимками кандидата. | Обслуживает отдельную диагностическую программу; пользовательский flow не строится на probe. |
| `internal/git` | Контроль состояния Git-репозитория. | Снимки дерева, index и submodules, сравнение изменений, проверка границы записи и точечное восстановление файлов. | Не выбирает политику flow; потребитель задаёт допустимую область изменений. |
| `internal/platformsupport` | Проверка поддерживаемой платформы. | Валидирует сочетание OS и архитектуры. | Не запускает процессы и не выбирает агентский провайдер. |
| `internal/processjob` | Управление дочерними процессами. | Удерживает и завершает дерево процессов средствами платформы. | Не знает протоколы Codex, Nessy или команды проверок. |
| `internal/agentruntime/conformance` | Общие контрактные проверки адаптеров. | Поставляет тестовые сценарии runtime и политик записи. | Используется тестами; исходящие зависимости не заданы `arch-go.yml`. |
| `internal/architecture` | Архитектурные тесты. | Проверяет `arch-go.yml` и build tags интеграционных тестов. | Содержит только тестовый код; внутренние импорты запрещены конфигурацией. |
| `internal/testscope` | Выбор интеграционных наборов для CI. | Классифицирует изменённые пути по Git и process suite. | Не запускает тесты; исходящие зависимости не заданы `arch-go.yml`. |
| `internal/testscope/cmd` | CLI для CI-классификатора. | Принимает событие и список файлов, печатает выбранные наборы. | Передаёт классификацию `internal/testscope`; исходящие зависимости не заданы `arch-go.yml`. |

Источник правил зависимостей: [`arch-go.yml`](../../arch-go.yml). Назначение и границы сверены с текущими Go-пакетами; при изменении кода и правил эту карту нужно обновить.
