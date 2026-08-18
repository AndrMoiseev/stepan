# Requirements

## Requirements

### REQ-001 — Явный запуск
Statement: Flow должен начинаться только по явному запросу пользователя на создание новой SDD-спецификации.
Verification: Запрос без явного намерения начать SDD не создаёт каталог спецификации, артефакты или состояние.

### REQ-002 — Последовательность стадий
Statement: Flow должен подготавливать артефакты в порядке `idea → requirements → design → plan`.
Verification: Для каждого допустимого перехода журнал наблюдений показывает, что ни одна стадия не началась до утверждения требуемой предыдущей стадии.

### REQ-003 — Итоговый пакет
Statement: Успешно завершённый flow должен оставлять утверждённый, проревьюенный и трассируемый пакет спецификации.
Verification: Финальная проверка находит четыре непустых артефакта, актуальные обязательные review-результаты, approvals и сквозные ссылки `REQ-* → DES-* → STEP-*`.

### REQ-004 — Финальное состояние
Statement: После утверждения плана flow должен иметь `stage: plan` и `status: approved`.
Verification: Утверждение валидного плана приводит ровно к этой паре значений и не запускает стадию реализации.

### REQ-005 — Репозиторий как источник истины
Statement: Канонические файлы пакета должны содержать все данные, необходимые для определения следующего допустимого действия без истории чата.
Verification: Новая host-сессия без предыдущего диалога восстанавливает тот же checkpoint и набор допустимых действий только по файлам репозитория.

### REQ-006 — Свежий агент роли
Statement: Каждый запуск содержательной SDD-роли должен выполняться новым агентом.
Verification: Трасса двух последовательных запусков одной роли содержит разные агентные контексты.

### REQ-007 — Минимальный launch envelope
Statement: Агент роли должен получать только role prompt, `spec-id`, объявленные для этой роли project inputs, явно перечисленные артефакты, применимые принятые риски, сведения о revision при её наличии, ожидаемый результат и разрешённую область записи.
Verification: Записанный launch envelope не содержит иных файлов, сообщений или метаданных.

### REQ-008 — Изоляция истории диалога
Statement: Содержательная роль не должна получать историю родительского диалога или разговор предыдущего агента.
Verification: Canary-строки, присутствующие только в таких диалогах, отсутствуют во входе и результате новой роли.

### REQ-009 — Блокирующий вопрос
Statement: Если без решения пользователя нельзя получить корректный артефакт, роль должна вернуть один минимальный блокирующий вопрос вместо предположения.
Verification: Сценарий с одним материально неоднозначным решением возвращает один вопрос и не публикует новый артефакт.

### REQ-010 — Область записи автора
Statement: Framer, Specifier, Designer и Planner должны иметь право изменять только принадлежащий роли артефакт текущей стадии.
Verification: Попытка роли изменить sentinel вне разрешённого артефакта отклоняется до публикации результата.

### REQ-011 — Read-only reviewer
Statement: Spec Reviewer не должен изменять ни один файл репозитория.
Verification: Снимок файлов до и после reviewer-run совпадает, включая review-файл самого reviewer.

### REQ-012 — Владение состоянием
Statement: Только Router должен публиковать изменения durable state.
Verification: Любое изменение state, сделанное содержательной ролью или reviewer, отклоняется до перехода.

### REQ-013 — Владение review-файлами
Statement: Только Router должен публиковать структурированный результат reviewer в канонический review-файл.
Verification: Reviewer возвращает данные Router, после чего файл появляется как Router-owned публикация.

### REQ-014 — Проверка границы изменений
Statement: Router должен проверить фактический набор изменений каждого role-run до публикации его результата.
Verification: Role-run с одним лишним изменением не меняет канонический артефакт или логическое состояние.

### REQ-015 — Каталог пакета
Statement: Канонический пакет изменения должен храниться в `.stepan/specs/<spec-id>/`.
Verification: Все version-controlled данные одного flow находятся под каталогом его `spec-id`.

### REQ-016 — Набор канонических файлов
Statement: Пакет должен использовать имена `idea.md`, `requirements.md`, `design.md`, `plan.md`, `state.yaml`, `review/requirements.yaml`, `review/design.yaml` и `review/plan.yaml` для соответствующих канонических данных.
Verification: Проверка завершённого пакета находит каждый тип данных по указанному пути и не требует альтернативного index-файла.

### REQ-017 — Ленивое создание
Statement: Файл стадии или review должен создаваться только при первой валидной публикации его содержимого.
Verification: До достижения стадии соответствующий путь отсутствует, а пустых placeholder-файлов нет.

### REQ-018 — Контракт idea
Statement: `idea.md` должен быть непустым Markdown с разделами `# Idea`, `## Problem`, `## Goal`, `## Target user`, `## Expected outcome`, `## Scope`, `### In` и `### Out`.
Verification: Структурная проверка отклоняет idea с отсутствующим или пустым обязательным разделом.

### REQ-019 — Контракт requirements
Statement: `requirements.md` должен состоять из `# Requirements`, `## Requirements`, записей `### REQ-NNN — <title>` с полями `Statement` и `Verification`, а также `## Boundaries and assumptions`.
Verification: Структурная проверка отклоняет файл с отсутствующей обязательной частью или дублирующимся `REQ-*`.

### REQ-020 — Атомарность requirement
Statement: Каждая запись `REQ-*` должна содержать одну нормативную обязанность и одну наблюдаемую проверку этой обязанности.
Verification: Review помечает blocking каждую запись, которая объединяет независимо реализуемые обязанности или не имеет наблюдаемого критерия.

### REQ-021 — Стабильность идентификаторов
Statement: После первой публикации идентификатор `REQ-*`, `DES-*` или `STEP-*` не должен переназначаться другой сущности.
Verification: Сравнение revisions показывает сохранение смысловой привязки идентификаторов; удалённые номера не переиспользуются.

### REQ-022 — Контракт design
Statement: `design.md` должен содержать обзор, решения `DES-*` с явным покрытием `REQ-*`, затрагиваемые компоненты, ограничения, риски, компромиссы и способы проверки.
Verification: Структурная проверка отклоняет design без любого обязательного вида сведений.

### REQ-023 — Покрытие design
Statement: Каждый `REQ-*` должен быть покрыт хотя бы одним `DES-*`.
Verification: Автоматическая проверка множества ссылок не находит непокрытого `REQ-*`.

### REQ-024 — Контракт plan
Statement: `plan.md` должен содержать упорядоченные шаги `STEP-*` с покрываемыми `REQ-*` и `DES-*`, одним проверяемым outcome, ожидаемыми изменениями и проверкой, а также финальную проверку пакета.
Verification: Структурная проверка отклоняет план без любого обязательного поля или с шагом, имеющим несколько независимых outcomes.

### REQ-025 — Покрытие plan
Statement: Каждый `REQ-*` и `DES-*` должен быть покрыт хотя бы одним `STEP-*`.
Verification: Автоматическая проверка множества ссылок не находит непокрытого требования или design decision.

### REQ-026 — Отсутствие скрытого redesign
Statement: Plan не должен вводить техническое решение, отсутствующее в утверждённом design.
Verification: Финальное review выдаёт blocking finding для каждого нового технического решения, встречающегося только в plan.

### REQ-027 — Provider-neutral артефакты
Statement: Канонические артефакты, state и reviews не должны содержать agent-, model-, provider- или thread-specific identifiers.
Verification: Проверка канонического пакета после Codex-run не находит таких identifiers.

### REQ-028 — Версионирование служебных схем
Statement: `state.yaml` и каждый review-файл должны указывать целочисленный `schema_version`.
Verification: Reader отклоняет служебный файл без версии схемы.

### REQ-029 — Неизвестная версия схемы
Statement: Reader должен остановить flow без записи при неизвестной будущей версии схемы или невозможности миграции без потери смысла.
Verification: Fixture с неподдерживаемой версией не изменяется и возвращает явную ошибку совместимости.

### REQ-030 — Каноническое хеширование
Statement: Router должен получать canonical SHA-256 артефактов, reviews, manifests и provenance-входов только через `../../../embedded-framework/skills/sdd/scripts/sdd.py`.
Verification: Проверка adapter-кода не находит второй реализации canonicalization или hashing, а golden fixtures обрабатываются helper-скриптом.

### REQ-031 — Ограничение helper
Statement: `../../../embedded-framework/skills/sdd/scripts/sdd.py` должен использовать только Python 3 standard library для генерации `spec-id` и canonical hashing.
Verification: Helper выполняет golden fixtures в чистом Python 3 environment без внешних пакетов.

### REQ-032 — Baseline approval
Statement: Approval стадии должен ссылаться на canonical hash утверждаемого артефакта, актуального обязательного review, применимых project-input manifests и активных accepted-risk records.
Verification: Изменение любого перечисленного входа делает approval неактуальным при следующей проверке.

### REQ-033 — Неизменяемость утверждённого входа
Statement: Последующая роль должна рассматривать утверждённый артефакт как read-only вход.
Verification: Попытка downstream-роли изменить утверждённый upstream-артефакт отклоняется и переводится в предусмотренный decision flow.

### REQ-034 — Источник spec-id
Statement: Источником `spec-id` должен быть полный исходный текст пользовательского запроса, которым явно начат новый flow, в полученном порядке, без transport metadata, пересказа Router или результата Framer.
Verification: Два запуска с одинаковым исходным пользовательским текстом передают helper идентичный source payload независимо от ответов Framer.

### REQ-035 — Захват исходного запроса
Statement: Router должен зафиксировать source payload и его canonical hash до запуска Framer.
Verification: После прерывания перед Framer новая сессия восстанавливает тот же source hash без истории чата.

### REQ-036 — Делегированная генерация spec-id
Statement: Router должен передать зафиксированный source payload в `../../../embedded-framework/skills/sdd/scripts/sdd.py` и использовать возвращённый helper результат без собственной нормализации.
Verification: Golden sources дают один и тот же `spec-id` при прямом вызове helper и через Router.

### REQ-037 — Разрешение коллизии spec-id
Statement: Если вычисленный `spec-id` уже существует, Router должен предложить пользователю продолжить существующий flow или создать новый каталог с наименьшим свободным суффиксом `-2`, `-3`, …, вычисленным helper с соблюдением его лимита длины.
Verification: Fixture с занятыми base, `-2` и `-4` предлагает resume либо новый ID `-3`; без выбора существующий пакет не изменяется.

### REQ-038 — Repository-scoped конфигурации ролей
Statement: Codex adapter должен иметь repository-scoped `.codex/agents/*.toml` для Router, Framer, Specifier, Designer, Planner и Spec Reviewer.
Verification: Проверка overlay находит отдельную agent-конфигурацию для каждой перечисленной роли.

### REQ-039 — Владение настройками роли
Statement: Agent TOML каждой роли должен задавать её project inputs, model, reasoning effort и sandbox settings.
Verification: Для каждой роли effective configuration воспроизводимо определяется из её repository-scoped TOML без выбора этих значений во время запуска.

### REQ-040 — Отсутствие глобальных путей project inputs
Statement: Portable protocol не должен предписывать общий набор путей к проектным документам.
Verification: Protocol проходит проверку в двух репозиториях с разными role-specific input paths без изменения общего текста protocol.

### REQ-041 — Progressive disclosure project inputs
Statement: Project inputs должны разрешаться и загружаться только при выполнении роли, которая объявляет их в своей agent-конфигурации.
Verification: Canary в input другой роли отсутствует в launch envelope текущей роли и в предварительном контексте Router.

### REQ-042 — Manifest project inputs
Statement: Каждый role-run должен сформировать детерминированный manifest фактически использованных project inputs с идентификатором роли, repository-relative путями, признаком отсутствия, canonical content hashes и hash декларации входов из agent-конфигурации.
Verification: Повторный запуск на неизменном checkout даёт тот же canonical manifest hash, а изменение содержимого, наличия, пути или декларации меняет его.

### REQ-043 — Сохранение provenance project inputs
Statement: Публикация результата роли должна сохранять canonical hash её project-input manifest как provenance этого результата.
Verification: Для каждого опубликованного артефакта и review можно однозначно найти manifest hash породившего role-run.

### REQ-044 — Проверка staleness project inputs
Statement: Перед использованием артефакта, review или approval система должна сравнить сохранённый project-input manifest с актуальным manifest соответствующей роли.
Verification: Изменение одного фактически использованного project input обнаруживается до следующего перехода, использующего зависимый результат.

### REQ-045 — Provenance принятого риска
Statement: Accepted-risk record должен содержать stage, finding ID, canonical hash review, canonical hashes входов review, точное решение пользователя и опциональный комментарий.
Verification: Для принятого риска можно восстановить finding и полный набор версий входов, относительно которых решение было принято.

### REQ-046 — Неизменяемость принятого риска
Statement: После публикации accepted-risk record не должен редактироваться или переиспользоваться для другого finding.
Verification: Изменение решения создаёт новую запись, а прежняя остаётся доступной как provenance.

### REQ-047 — Передача принятого риска downstream-роли
Statement: Каждый активный accepted-risk record утверждённого upstream-входа должен передаваться каждой зависимой downstream-роли.
Verification: Launch envelope Designer и Planner содержит применимые записи с теми же canonical hashes, что и approval.

### REQ-048 — Передача принятого риска reviewer
Statement: Каждый активный accepted-risk record проверяемого пакета должен быть входом соответствующего downstream review.
Verification: Review provenance перечисляет hashes применимых accepted-risk records, а reviewer может сослаться на их исходные finding IDs.

### REQ-049 — Инвалидация принятого риска
Statement: Изменение исходного review, любого его provenance-входа или текста finding должно деактивировать связанный accepted-risk record для будущих переходов.
Verification: После такого изменения прежняя запись сохраняется для аудита, но больше не удовлетворяет guard approval или continue.

### REQ-050 — Обязательные reviews
Statement: Requirements и design должны пройти stage review, а plan должен пройти review полного пакета до пользовательского checkpoint.
Verification: Перейти к approval соответствующей стадии без требуемого review невозможно.

### REQ-051 — Независимый reviewer-run
Statement: Каждое первоначальное или повторное review должно выполняться новым Spec Reviewer.
Verification: Два review одного revision имеют независимые агентные контексты.

### REQ-052 — Контракт review
Statement: Review-результат должен содержать stage, verdict, provenance всех прочитанных артефактов, project-input manifest hash reviewer, hashes активных accepted-risk records и список findings со стабильным ID, severity, references, problem и recommendation.
Verification: Schema validation отклоняет review без любого обязательного поля.

### REQ-053 — Согласованность verdict и findings
Statement: Review должен иметь `verdict: pass` тогда и только тогда, когда он не содержит findings с `severity: blocking`; при наличии хотя бы одного такого finding verdict должен быть `changes-required`.
Verification: Schema или semantic validation отклоняет `pass` с blocking finding и `changes-required` без blocking finding, включая review только с advisory findings.

### REQ-054 — Семантика advisory
Statement: Advisory finding не должен блокировать пользовательский checkpoint; review с advisory findings и без blocking findings должен иметь `verdict: pass`.
Verification: Review только с advisory findings получает `verdict: pass` и переходит по REQ-065 в `awaiting-approval`.

### REQ-055 — Стабильность finding ID
Statement: Неустранённый finding должен сохранять свой ID в следующем review той же стадии.
Verification: Сравнение последовательных review показывает прежний ID у семантически того же неустранённого finding.

### REQ-056 — Актуальный review snapshot
Statement: Канонический review-файл стадии должен представлять только последний полностью опубликованный review snapshot.
Verification: После повторного review файл содержит новый полный результат, а устранённые findings отсутствуют.

### REQ-057 — Stale review
Statement: Несовпадение любого сохранённого provenance hash review с актуальным входом должно сделать review неактуальным.
Verification: Изменение артефакта, manifest или accepted-risk input запрещает использовать прежний verdict для approval.

### REQ-058 — Пространство логических состояний
Statement: Durable state должен использовать stages `idea`, `requirements`, `design`, `plan` и statuses `drafting`, `reviewing`, `revising`, `awaiting-approval`, `awaiting-decision`, `approved`, `aborted`.
Verification: State schema принимает только перечисленные значения и допустимые их сочетания.

### REQ-059 — Данные перехода
Statement: Durable state должен содержать текущие stage и status, счётчик автоматических revisions, approvals, pending decision при его наличии и сведения, необходимые для crash reconciliation.
Verification: После завершения host-процесса reader определяет guards и следующий event без памяти процесса.

### REQ-060 — Переход start
Statement: При event `start-new` из отсутствующего flow Router должен опубликовать `stage: idea`, `status: drafting`, нулевой revision counter, пустые approvals и provenance исходного запроса до запуска Framer.
Verification: Прерывание сразу после начальной публикации возобновляется с запуском Framer для того же source hash.

### REQ-061 — Публикация idea
Statement: При успешном результате Framer из `idea/drafting` Router должен опубликовать валидную idea и перейти в `idea/awaiting-approval`.
Verification: Успешный Framer-run показывает checkpoint idea, не создавая review idea.

### REQ-062 — Публикация reviewable-артефакта
Statement: При успешном результате автора из `drafting` или `revising` на стадии requirements, design или plan Router должен опубликовать валидный артефакт и перейти в `reviewing` той же стадии.
Verification: Для каждой из трёх стадий успешный author-run немедленно делает следующим допустимым действием независимое review.

### REQ-063 — Переход к clarification
Statement: При блокирующем вопросе автора из `drafting` или `revising` Router должен перейти в `awaiting-decision` той же стадии с durable `pending.kind: clarification` и текстом вопроса.
Verification: После restart тот же вопрос отображается без повторного запуска автора.

### REQ-064 — Ответ на clarification
Statement: При event `answer-clarification` из `awaiting-decision` с `pending.kind: clarification` Router должен durable-сохранить ответ и перейти в `drafting` той же стадии перед запуском нового автора.
Verification: Crash после сохранения ответа не требует повторного ввода и не теряет ответ.

### REQ-065 — Успешное review
Statement: При согласованном по REQ-053 `verdict: pass` из `reviewing` Router должен атомарно опубликовать review snapshot и перейти в `awaiting-approval` той же стадии.
Verification: После перехода checkpoint показывает hashes именно опубликованного review и его входов.

### REQ-066 — Автоматическая revision
Statement: При согласованном по REQ-053 `changes-required` из `reviewing` и счётчике меньше трёх Router должен опубликовать review, увеличить счётчик на один и перейти в `revising` той же стадии с findings как revision input.
Verification: Первоначальное неуспешное review запускает attempt 1, а не attempt 2.

### REQ-067 — Возврат revision на review
Statement: При успешной обработке автоматической revision Router должен перейти из `revising` в `reviewing` той же стадии после публикации новой версии артефакта.
Verification: Ни одна revised-версия не достигает пользовательского checkpoint без нового reviewer-run.

### REQ-068 — Исчерпание revision limit
Statement: При `changes-required` из `reviewing` со счётчиком три Router должен опубликовать review и перейти в `awaiting-decision` с `pending.kind: unresolved-findings` без запуска четвёртой автоматической revision.
Verification: Сценарий initial review плюс три неуспешных revision-review цикла останавливается после третьего цикла.

### REQ-069 — Пользовательская revision
Statement: Event `revise(feedback)` должен быть допустим из `awaiting-approval`, а из `awaiting-decision` — только для `pending.kind: unresolved-findings`, `upstream-revision`, `manual-mutation`, `stale-input` или `commit-conflict`; при успешном event Router должен durable-сохранить feedback, закрыть применимый pending и, только для `commit-conflict`, commit intent, сбросить automatic revision counter в ноль и перейти на stage, указанную pending или текущим checkpoint, со `status: drafting` для idea либо `status: revising` для requirements, design или plan.
Verification: Для каждого перечисленного источника event новый автор получает сохранённый feedback после restart с attempt 0, а иной `pending.kind` отклоняет `revise(feedback)` без durable mutation.

### REQ-070 — Принятие blocking findings
Statement: При event `accept-risk(ids)` из `awaiting-decision` с `pending.kind: unresolved-findings` Router должен перейти в `awaiting-approval` только если опубликованы accepted-risk records для всех текущих blocking findings.
Verification: Пропущенный или stale finding ID сохраняет `awaiting-decision`; полный актуальный набор открывает approval.

### REQ-071 — Guard continue
Statement: Event `continue` должен быть допустим в `awaiting-approval` только для валидного текущего артефакта с актуальным обязательным review, у которого нет непринятого blocking finding.
Verification: Каждый нарушенный guard отклоняет event без изменения approvals, stage или status.

### REQ-072 — Утверждение промежуточной стадии
Statement: При допустимом `continue` на idea, requirements или design Router должен опубликовать approval и перейти к следующей stage со `status: drafting` и нулевым revision counter.
Verification: Для каждой промежуточной стадии approval hash сохранён до наблюдаемого запуска следующего автора.

### REQ-073 — Утверждение plan
Statement: При допустимом `continue` на plan Router должен опубликовать approval и перейти в `plan/approved`.
Verification: Финальное состояние содержит актуальные approvals всего пакета и не запускает новую роль.

### REQ-074 — Немутационный question
Statement: Event `question(text)` в пользовательском checkpoint не должен изменять канонические артефакты, reviews, approvals, stage или status.
Verification: Canonical hashes до и после ответа на вопрос совпадают.

### REQ-075 — Stop
Statement: Event `stop` в любом пользовательском checkpoint должен завершить текущую host-сессию без перехода durable state.
Verification: Следующий явный resume показывает тот же checkpoint и допустимые действия.

### REQ-076 — Resume
Statement: Event `resume(spec-id)` должен сначала выполнить reconciliation, а затем повторить сохранённый checkpoint или продолжить единственную незавершённую crash-safe операцию.
Verification: Повторные resume без новых входов сходятся к одному stage, status и checkpoint.

### REQ-077 — Abort
Statement: Event `abort` из любого незавершённого состояния должен перейти в status `aborted` текущей stage без удаления опубликованных данных.
Verification: После abort канонические файлы остаются доступными, а автоматическая работа не запускается.

### REQ-078 — Терминальность abort
Statement: Из status `aborted` должны быть запрещены все мутирующие events этого flow.
Verification: Попытки continue, revise, accept-risk, retry или запуска роли возвращают diagnostic без изменения файлов.

### REQ-079 — Недопустимый переход
Statement: Event, отсутствующий в контракте для текущих stage, status, pending kind и guards, должен завершаться diagnostic без durable mutation.
Verification: Model-based test перебирает все неописанные комбинации и подтверждает неизменность canonical hashes.

### REQ-080 — Поздняя проблема upstream
Statement: При материальном finding поздней роли об утверждённом upstream-артефакте Router должен перейти к stage владельца в `awaiting-decision` с `pending.kind: upstream-revision` и provenance причины возврата.
Verification: Пользователь видит исходный downstream finding, а upstream-файл не изменяется до решения.

### REQ-081 — Подтверждённый возврат
Statement: При event `revise(feedback)` из `awaiting-decision` с `pending.kind: upstream-revision` Router должен удалить активные approvals этой и всех последующих стадий и перейти на stage владельца со `status: drafting` для idea либо `status: revising` для requirements, design или plan.
Verification: Более ранние независимые approvals сохраняются, а изменяемая и downstream-стадии требуют новых checkpoints.

### REQ-082 — Сохранение stale downstream-файлов
Statement: При возврате к upstream-стадии существующие downstream-артефакты и reviews должны сохраняться как stale до их явной revision.
Verification: Файлы не удалены, но их прежние hashes не удовлетворяют guards review или approval.

### REQ-083 — Ручное изменение
Statement: При обнаружении ручного изменения утверждённого артефакта Router должен перейти на stage владельца в `awaiting-decision` с `pending.kind: manual-mutation`, durable evidence прежнего и обнаруженного hashes, затронутыми путями и исходным checkpoint без принятия или отката изменения.
Verification: Пользовательские bytes сохраняются, прежний approval не действует, revision counter не изменяется, а следующий разрешающий мутирующий event — `revise(feedback)` из REQ-108 либо `abort` из REQ-077.

### REQ-084 — Stale project input
Statement: При несовпадении project-input manifest Router должен перевести earliest affected stage в `awaiting-decision` с `pending.kind: stale-input`, durable evidence прежнего и текущего manifests и списком affected stages, не меняя revision counter, и деактивировать зависимые reviews, accepted risks и approvals.
Verification: Изменение input Planner не инвалидирует design, изменение input Specifier инвалидирует requirements и все зависимые стадии, а pending содержит достаточно данных для guard REQ-109 после restart.

### REQ-085 — Ошибка role-run
Statement: Если role-run завершается без валидного опубликованного результата, Router должен сохранить предыдущее логическое состояние и разрешить идемпотентный retry нового агента.
Verification: Ошибка до публикации не увеличивает revision counter и не оставляет канонический частичный результат.

### REQ-086 — Нарушение write boundary
Statement: При нарушении write boundary Router должен отклонить role-result и перейти на той же stage в `awaiting-decision` с `pending.kind: boundary-violation`, сохранив originating status, pre-run hashes, provenance входов, diagnostic и обнаруженные пути без изменения revision counter.
Verification: Запрещённое изменение не считается результатом стадии, а сохранённые данные однозначно определяют guard и target event `retry-role` из REQ-110 после restart.

### REQ-087 — Complete transition contract
Statement: Реализация state machine должна содержать исчерпывающую таблицу комбинаций current stage, current status, pending kind, event, guard outcome, resulting stage/status, durable side effects, counter mutation и retry behavior для REQ-060—REQ-100 и REQ-108—REQ-113, включая commit, recovery, reconciliation и invalid-event поведение REQ-079.
Verification: Model-based test перебирает каждый описанный и неописанный event для каждой комбинации stage/status/pending, подтверждает все табличные переходы и доказывает отсутствие дополнительных мутирующих переходов.

### REQ-088 — Guard continue-and-commit
Statement: Event `continue-and-commit` должен иметь те же approval guards, что и `continue`.
Verification: Любой вход, блокирующий `continue`, также блокирует commit до изменения Git index или HEAD.

### REQ-089 — Область approval commit
Statement: Approval commit должен включать только `.stepan/specs/<spec-id>/` и иметь сообщение `sdd(<spec-id>): approve <stage>`.
Verification: Diff созданного commit не содержит путь вне пакета, а commit message совпадает побайтно.

### REQ-090 — Завершение continue-and-commit
Statement: После создания и проверки ожидаемого commit успешный `continue-and-commit` должен durable-опубликовать approval, завершить commit intent, очистить pending, установить automatic revision counter в ноль и перейти из idea, requirements или design в следующую stage со `status: drafting`, а из plan — в `plan/approved`; до проверки commit этот target не должен становиться наблюдаемым.
Verification: Для каждой из четырёх stages проверка фиксирует ровно один ожидаемый commit и указанный target с нулевым counter, а forced crash до проверки commit возобновляется на том же approval checkpoint или в commit reconciliation без запуска следующей роли и без автоматического retry.

### REQ-091 — Обычная ошибка Git
Statement: Если commit не создан и входы не изменились, ошибка Git должна durable-зафиксировать diagnostic завершённой попытки и оставить ту же stage в `awaiting-approval` с неизменным revision counter и возможностью нового явного `continue-and-commit`, обычного `continue`, `revise(feedback)`, `stop` или `abort`, но без автоматического retry.
Verification: Симулированный отказ Git сохраняет текущие approval guards и counter, не создаёт commit, а новая попытка начинается только новым event.

### REQ-092 — Конфликт во время commit
Statement: Если во время commit-операции обнаружено неожиданное изменение package, index, worktree или HEAD, Router должен оставить approval stage текущей, перейти в `awaiting-decision` с `pending.kind: commit-conflict` и durable-сохранить identity попытки, pre-operation и observed snapshots, целевой approval transition и diagnostic без изменения revision counter.
Verification: Симулированное внешнее изменение останавливает переход без автоматического retry или отката пользовательских данных, а pending однозначно поддерживает events REQ-111—REQ-113 после restart.

### REQ-093 — Recoverable publication boundary
Statement: Каждая логическая публикация артефакта, review, state transition или approval должна либо стать полностью проверяемой, либо оставить предыдущую валидную версию и достаточный durable intent для reconciliation.
Verification: Crash injection после каждой durable write не создаёт состояние, в котором partial data принимаются как завершённый переход.

### REQ-094 — Идемпотентная reconciliation
Statement: Reconciliation одного и того же набора repository files, index и HEAD должна быть идемпотентной.
Verification: Два последовательных resume без внешних изменений дают одинаковые canonical hashes, state и checkpoint.

### REQ-095 — Повторное использование опубликованного результата
Statement: Recovery должен повторно использовать уже опубликованный и provenance-валидный generative artifact вместо его регенерации.
Verification: Crash после публикации артефакта не запускает автора повторно и сохраняет hash артефакта.

### REQ-096 — Непубликованный частичный результат
Statement: Recovery не должен продвигать неполный или не прошедший write-boundary validation role-result в каноническое состояние.
Verification: Fixture с оборванной записью или запрещённым соседним изменением возвращается к предыдущей валидной границе либо к явному decision, не к следующей стадии.

### REQ-097 — Durable commit intent
Statement: До изменения Git для `continue-and-commit` Router должен durable-сохранить identity попытки, исходные stage, status и revision counter, pre-operation HEAD, ожидаемый package tree, commit message и целевой approval transition.
Verification: После crash reconciliation имеет достаточно данных отличить не начатый, завершённый и конфликтующий commit.

### REQ-098 — Reconciliation state и HEAD
Statement: Если ожидаемый approval commit уже достижим из HEAD и точно соответствует durable intent, recovery должен без второго commit завершить intent, очистить pending и опубликовать точный target stage/status из REQ-090 с нулевым revision counter.
Verification: Crash после создания commit, но до обновления state, приводит к одному commit, завершённому intent и корректному следующему checkpoint.

### REQ-099 — Отсутствующий approval commit
Statement: Если state указывает на незавершённый commit, но HEAD остался на сохранённом pre-operation значении и остальные guard inputs совпадают, recovery должен durable-завершить попытку как `not-created`, вернуть исходную stage в `awaiting-approval` с сохранённым revision counter и не создавать commit до нового явного `continue-and-commit`.
Verification: Fixture с intent без commit восстанавливает точный approval checkpoint и counter, не меняет HEAD и не выполняет автоматический retry.

### REQ-100 — Неоднозначное расхождение state и HEAD
Statement: Если state/HEAD disagreement не соответствует ни завершённому, ни не начатому commit intent, recovery должен оставить исходную approval stage, перейти в `awaiting-decision` с `pending.kind: commit-conflict`, evidence расхождения и сохранённым revision counter без изменения Git.
Verification: Fixture с посторонним HEAD возвращает тот же durable pending при повторном resume, сохраняет repository snapshot и допускает только табличные events REQ-111—REQ-113 и общие немутирующие или terminal events.

### REQ-101 — Сохранение пользовательского index и worktree
Statement: Commit и recovery не должны добавлять, удалять, коммитить или откатывать существующие пользовательские либо hook-created изменения вне явно принадлежащих Router временных операций.
Verification: Сценарий с заранее staged, unstaged и hook-created файлами сохраняет их содержимое и staging disposition после успеха, ошибки и crash recovery.

### REQ-102 — Ограничение byte-identical проверок
Statement: Проверка byte-identical результата должна применяться только к deterministic helper fixtures или к повторному чтению уже опубликованных outputs, но не к повторной генерации содержательной ролью.
Verification: Resume-тесты сравнивают hashes опубликованных outputs, не требуют совпадения двух независимых generative runs и всё же требуют побайтного совпадения helper fixtures.

### REQ-103 — Crash matrix
Statement: Forward-tests должны принудительно прерывать процесс во время каждого role-run и после каждой durable границы artifact, review, state, approval, index и commit.
Verification: Для каждой точки прерывания новая host-сессия достигает того же допустимого checkpoint, что и непрерывный контрольный проход.

### REQ-104 — Golden scenarios adapter
Statement: Один набор provider-neutral golden scenarios должен выполняться через fake runner и первый Codex adapter.
Verification: Оба runner проходят одинаковые проверки переходов, guards, provenance, write boundaries и recovery.

### REQ-105 — Доказательство переносимости
Statement: Подтверждение переносимости на вторую реальную агентную систему должно требовать прохождения тех же golden scenarios её adapter.
Verification: Результат fake runner отдельно помечается недостаточным доказательством второго реального adapter.

### REQ-106 — Проверка чистого контекста Codex
Statement: Codex adapter должен пройти canary-тест отсутствия неразрешённого parent и prior-agent context во входе новой роли.
Verification: Ни одна canary-строка вне разрешённых inputs не появляется в launch envelope или output.

### REQ-107 — Проверка sandbox Codex
Statement: Codex adapter должен отвергать результат автора или reviewer, нарушающий configured sandbox/write boundaries, до state transition.
Verification: Sentinel-тест автора и read-only тест reviewer не оставляют запрещённых канонических изменений.

### REQ-108 — Resolution manual-mutation
Statement: Event `revise(feedback)` из `awaiting-decision` с `pending.kind: manual-mutation` должен быть допустим только когда все обнаруженные пути принадлежат артефакту stage владельца и отсутствует иное unreconciled condition; успешный event должен сохранить пользовательские bytes, durable-сохранить их актуальные hashes и feedback как revision input, инвалидировать review, accepted risks и approvals этой и последующих stages, очистить pending, сбросить revision counter в ноль и перейти на stage владельца со `status: drafting` для idea либо `status: revising` для requirements, design или plan.
Verification: При успешном event новый автор получает принятые manual bytes и feedback с attempt 0; нарушенный guard оставляет прежние stage/status, pending и counter без durable mutation, после чего event допускает только новый явный retry.

### REQ-109 — Resolution stale-input
Statement: Event `revise(feedback)` из `awaiting-decision` с `pending.kind: stale-input` должен быть допустим только когда все обязательные project inputs читаемы и вычисленный manifest остаётся неизменным до публикации transition; успешный event должен durable-опубликовать новый manifest и feedback как revision input, сохранить инвалидации зависимых reviews, accepted risks и approvals, очистить pending, сбросить revision counter в ноль и перейти на earliest affected stage со `status: drafting` для idea либо `status: revising` для requirements, design или plan.
Verification: Stable manifest запускает нового автора с attempt 0 и новым manifest hash; отсутствующий или повторно изменившийся input оставляет прежние stage/status, pending и counter без durable mutation и без автоматического retry.

### REQ-110 — Resolution boundary-violation
Statement: Event `retry-role` из `awaiting-decision` с `pending.kind: boundary-violation` должен быть допустим только когда все запрещённые пути восстановлены до сохранённых pre-run hashes и provenance разрешённых inputs не изменился; успешный event должен очистить pending, вернуться на сохранённые stage и originating status и запустить ровно один новый agent той же роли без изменения revision counter.
Verification: Нарушенный guard оставляет pending и counter без durable mutation; успешный retry не расходует revision attempt, а повторная ошибка или violation обрабатывается как новая явная попытка по REQ-085 или новое pending без автоматического retry.

### REQ-111 — Отмена конфликтующего commit
Statement: Event `cancel-commit` из `awaiting-decision` с `pending.kind: commit-conflict` должен быть допустим только при актуальных approval guards и возможности удалить лишь Router-owned временные данные; успешный event должен durable-завершить commit intent как cancelled, сохранить conflict evidence, очистить pending и перейти на ту же stage в `awaiting-approval` без изменения revision counter, изменяя только перечисленные поля Router-owned `state.yaml` и Router-owned временные данные, необходимые для перехода, и сохраняя HEAD, pre-existing index, а также все пользовательские, hook-created и иные не принадлежащие Router данные worktree.
Verification: Fixture успешного cancel показывает ожидаемый diff только перечисленных полей `state.yaml` и удаление только Router-owned временных данных, сохраняет зафиксированные перед event HEAD, полный index и полную карту путей и bytes пользовательских, hook-created и иных не принадлежащих Router данных worktree, после чего допускает `continue`, `continue-and-commit` и `revise(feedback)`; fixture с нарушенным guard сохраняет прежние pending, intent и counter без durable mutation и автоматического retry.

### REQ-112 — Повтор конфликтующего commit
Statement: Event `retry-commit` из `awaiting-decision` с `pending.kind: commit-conflict` должен быть допустим только при guards REQ-088 и repository snapshot, позволяющем изолировать ожидаемый package commit без изменения пользовательских данных; успешный guard должен durable-создать новый intent от текущего snapshot и выполнить ровно одну commit-попытку, которая завершается по REQ-090, REQ-091 или REQ-092.
Verification: Успех приводит к target REQ-090 с нулевым counter, обычная ошибка — к REQ-091, новый конфликт — к новому pending REQ-092; до успеха revision counter не меняется и ни один исход не запускает автоматический retry.

### REQ-113 — Revision после commit-conflict
Statement: Event `revise(feedback)` из `awaiting-decision` с `pending.kind: commit-conflict` должен быть допустим только когда Router может отказаться от своих временных commit-операций; успешный event должен durable-завершить intent как abandoned, сохранить conflict evidence и feedback, очистить pending, сбросить revision counter в ноль и перейти на ту же stage со `status: drafting` для idea либо `status: revising` для requirements, design или plan, изменяя только перечисленные поля Router-owned `state.yaml` и Router-owned временные данные, необходимые для перехода, и сохраняя HEAD, pre-existing index, а также все пользовательские, hook-created и иные не принадлежащие Router данные worktree.
Verification: Снимок успешного event до запуска автора показывает ожидаемый diff только перечисленных полей `state.yaml` и изменение только Router-owned временных данных, сохраняет зафиксированные перед event HEAD, полный index и полную карту путей и bytes пользовательских, hook-created и иных не принадлежащих Router данных worktree, а следующий role-run запускает нового автора с attempt 0; fixture с нарушенным guard сохраняет pending, intent и counter без durable mutation и допускает только новый явный retry.

## Boundaries and assumptions

- Этот контракт охватывает только pre-development flow до `stage: plan`, `status: approved`; реализация, тестирование реализации, code review, rework кода и post-development flow находятся вне scope.
- Реализация Codex adapter и adapters других систем не входит в текущий результат спецификации; в scope входят их обязательная форма, interoperability constraints и критерии реализуемости.
- Фиксированными interoperability contracts считаются каталог `.stepan/specs/<spec-id>/`, имена канонических файлов из REQ-016, helper `../../../embedded-framework/skills/sdd/scripts/sdd.py` и repository-scoped agent overrides из REQ-038—REQ-039.
- Portable protocol остаётся provider-neutral. Project-specific document paths не являются частью общего protocol и существуют только в конфигурации конкретной роли.
- Design выбирает конкретные форматы launch envelope, project-input manifest, accepted-risk storage, transition table, publication intent и recovery journal при условии соблюдения наблюдаемых требований выше.
- Design выбирает механизм atomic publication и Git isolation; контракт не предполагает, что несколько filesystem/Git записей физически атомарны.
- История переходов, runtime logs, timestamps обновлений и agent session IDs не обязаны входить в version-controlled пакет, если durable state и provenance достаточны для REQ-005 и REQ-076.
- Idea не проходит отдельное агентное review: пользователь является источником намерения и ценности, а её approval остаётся обязательным checkpoint.
- Основные артефакты являются Markdown без обязательного YAML frontmatter; служебные state и review остаются machine-readable YAML по versioned schemas.
- Дополнительные разделы артефактов допустимы, если они содержательны и не ослабляют обязательные контракты.
