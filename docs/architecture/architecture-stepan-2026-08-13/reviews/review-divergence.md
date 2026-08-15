# Divergence review

## Verdict

**CHANGES REQUIRED.** Направление архитектуры согласованно, но четыре центральных контракта допускают несовместимые реализации: crash-boundary журнала/evidence, смысл `verified`, идентичность Git candidate и семантика `AccessPolicy`. Это не отложенные продуктовые опции: от них уже зависят recovery, acceptance и общий агентский порт.

## Две формально совместимые реализации

### Реализация A — буквальная минимальная

- `runstore` пишет строку JSON, вызывает sync и при replay считает любую неразбираемую последнюю строку оборванным хвостом. Evidence допускается записать после события, потому что в документе определена ссылка, но не порядок durability.
- Версия CLI между minimum и максимальной verified-версией считается verified; warning выдаётся только версии выше максимальной, буквально по AD-11.
- Candidate обозначается текущими `HEAD OID` и результатом обычного `git write-tree`, то есть фактически состоянием реального index. Новые unstaged/untracked файлы могут не попасть в tree OID.
- `policy` — открытая provider-neutral map. Codex и Claude исполняют распознанное подмножество; неизвестное поле отклоняется конкретным адаптером.
- `RunTurn` возвращает `(Result, error)`, но при любой ошибке `Result` пуст, включая `SessionRef` и evidence.

### Реализация B — строгая транзакционная

- Evidence сначала пишется во временный файл, синхронизируется и публикуется под неизменяемым именем; только затем коммитится ссылающееся событие. Игнорируется лишь последняя запись без завершающего `\n`; синтаксически неверная newline-terminated строка считается повреждением.
- Verified только точный tuple `agent × version × OS`, присутствующий в живой матрице; любая иная версия выше minimum — unverified с warning.
- Candidate tree строится через изолированный временный Git index из полного разрешённого набора tracked и untracked изменений, который Stepan собирается commit-ить.
- `AccessPolicy` — закрытый versioned value с deny-by-default семантикой и одинаковыми conformance cases для всех адаптеров.
- `RunTurn` на ошибке всё равно может вернуть session/evidence, если процесс успел стартовать.

Обе реализации могут сослаться на текущий текст spine, но они несовместимы по восстановлению run, статусу поддержки CLI, объекту acceptance и поведению общего runner.

## Реальные архитектурные разрывы

### 1. Не определена идентичность candidate — критично

AD-15 связывает проверки с `HEAD OID + tree OID`, но не говорит, какой tree имеется в виду. Обычный `HEAD^{tree}` не содержит рабочие изменения; `git write-tree` читает index и пропускает unstaged/untracked файлы. Также не определено, входят ли ignored outputs в правило «любое изменение инвалидирует результат».

**Ясная правка:** определить candidate как `base HEAD OID + tree OID` полного и ровно того write-set-approved набора файлов, который Stepan commit-ит. Tree строится через отдельный временный index, не изменяя пользовательский index; новые разрешённые файлы включаются. Изменение candidate означает изменение этого дерева, а ignored runtime outputs вне commit set не меняют candidate.

### 2. AD-11 и AD-12 по-разному определяют verified — критично

AD-11 требует warning только «выше последней проверенной версии». AD-12 разрешает verified только точной версии/platform после live conformance. Например, при minimum `1.0` и проверенных `1.0`, `1.2` статус непроверенной `1.1` из текста не следует однозначно.

**Ясная правка:** minimum отвечает только за допустимость запуска. Verified означает точное присутствие tuple `agent × exact version × OS` в матрице; любой допустимый, но отсутствующий tuple запускается как `unverified` с warning. «Выше последней проверенной» убрать.

### 3. Не замкнута crash-boundary события и evidence — критично

AD-2 задаёт commit события, AD-3 — ссылку на evidence, но не задаёт порядок их durability и неизменяемость уже упомянутого evidence. Также невозможно отличить повреждённую последнюю committed-запись от «оборванного хвоста», если критерий хвоста не определён.

**Ясная правка:** evidence полностью записывается, синхронизируется и публикуется как immutable до commit ссылающегося события. Event writer пишет полную JSON-запись с завершающим LF и затем sync; только последняя запись без LF считается неcommitted tail. Невалидная запись с LF — corruption и fail-closed. Replay проверяет containment path и SHA-256 до использования evidence.

### 4. Обязательный `policy` не имеет общей семантики — высокий риск

AD-5 требует от каждого адаптера выполнить весь contract/policy, а AD-8 требует одинаковых гарантий, но набор policy-полей, default и способ эволюции не закреплены. Два адаптера могут честно реализовать разные понятия `network deny`, read roots или write-set и оба назвать это общей policy.

**Ясная правка:** закрепить `AccessPolicy` как закрытый, versioned, provider-neutral value с deny-by-default семантикой; неизвестное или неисполняемое требование даёт `policy_violation` до запуска. Минимальные оси — разрешённые read roots, write set, network mode и execution-profile fingerprint. Provider-specific knobs не расширяют policy неявно. Точный wire/type shape может жить в единственном контракте `internal/agent`, но conformance fixtures обязаны проверять его семантику.

### 5. Runner описан концептуально, но не замкнут как Go-контракт — средний риск

Не определены сигнатура `Close`, типизированное представление `ErrorKind`, допустимость частичного `Result` при ошибке и судьба evidence/session после стартовавшего, но неуспешного turn. Адаптеры могут не сойтись на уровне compile-time и recovery.

**Ясная правка:** до параллельной разработки адаптеров зафиксировать в `internal/agent` один малый Go interface и error/result semantics. Минимально: `RunTurn(context.Context, Request) (Result, error)`, `Close() error`, общий typed error с `Kind`; если процесс был запущен, evidence о попытке сохраняется независимо от terminal kind. Решить и проверить fixture-тестом, возвращается ли частичный `Result` вместе с error.

### 6. Durable schema названа v1, но её канонический владелец не указан — средний риск

Типы полей envelope, начальное значение `seq`, формат времени и payload schemas могут разойтись между workflow-пакетами или версиями binary.

**Ясная правка:** объявить `runstore` единственным владельцем canonical event envelope/schema и golden replay fixtures; workflow передаёт типизированные domain records и не сериализует JSONL самостоятельно. Детали формата можно не раздувать в spine.

## Намеренно отложенные варианты — не дефекты spine

- Назначение agent adapter роли, если фактический runner/version/profile фиксируется до первого turn.
- Выбор extension profile. До решения нельзя реализовывать «универсальное наследование» как default; временный Codex baseline не должен превращаться в контракт Claude.
- Parallel worktrees, меж-run locking, второй storage backend и snapshots.
- Workflow DSL, внешний adapter ABI, Linux.
- Конкретные флаги и minimum Claude Code. До live conformance Claude остаётся docs-designed и не может объявляться supported/verified; это допустимая блокировка, а не повод угадать minimum.

## Допустимые различия реализаций

- Long-lived app-server у одного провайдера и process-per-turn у другого, если общий `RunTurn` observable contract соблюдён.
- Windows Job Object и macOS process groups/signals, если одинаково проходят lifecycle/cancel/cleanup conformance.
- Разные workflow retry/rework решения для разных effect semantics: классификация остаётся у адаптера, решение и durable event — у workflow.

## Минимальный набор правок перед `final`

1. Уточнить точный verified tuple вместо диапазона.
2. Закрыть durability-порядок evidence → event и критерий torn tail.
3. Определить candidate tree как полный будущий commit через isolated index.
4. Закрепить закрытую versioned `AccessPolicy` с deny-by-default.

Пункты 5–6 можно выполнить одной короткой нормой о единственном владельце контрактов в `internal/agent` и `runstore`, оставив точные Go/JSON типы реализации и conformance fixtures.

## Resolution check

**CHANGES REQUIRED.** Candidate identity и закрытая `AccessPolicy` теперь замкнуты; LF-критерий torn tail и порядок evidence → event определены. Остались две точечные правки:

1. AD-11 всё ещё говорит, что warning/`unverified` относится только к версии «выше последней проверенной», тогда как AD-12 правильно требует exact tuple. Заменить в AD-11 на: любая версия не ниже minimum запускается, но любой tuple, отсутствующий в live-матрице, получает warning и `unverified`.
2. В AD-3 уточнить, что «атомарно публикуется» означает **durable publish**, включая фиксацию directory metadata подходящим OS-механизмом до commit события; одного `fsync` временного файла и rename недостаточно для заявленной crash-boundary.

### Final resolution check

**PASS.** AD-3 теперь требует durable publish evidence до ссылающегося события и fail-closed проверку файла/hash; AD-11 согласован с exact-tuple правилом AD-12. Выявленные load-bearing расхождения закрыты.
