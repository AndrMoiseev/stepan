# Исполнение, проверка и evidence

## Исполнение и детерминированные gates

**EXEC-001 [P0].** Каждая попытка MUST исполняться в отдельной ветке/worktree от записанного base SHA; worker MUST NOT работать в основном checkout.

**EXEC-002 [P0].** До изменения Controller MUST сохранить baseline checks и известные failures с provenance.

**EXEC-003 [P0].** После изменения Controller MUST отличать новый candidate regression от существующего baseline failure.

**EXEC-004 [P0].** Implementation Worker MUST получить revision-bound brief, scoped code, write boundary и focused-check commands.

**EXEC-005 [P0].** Детерминированные gates MUST выполняться до модельной верификации: schema/format, boundary, build, lint/typecheck и focused tests в соответствии с project profile.

**EXEC-006 [P0].** Project profile MUST декларативно задавать именованные команды baseline, build, static checks, focused tests и full tests. Ядро Stepan MUST NOT содержать обязательной логики для конкретного языка, build tool или test framework.

**EXEC-007 [P0].** Generic command runner MUST фиксировать нормализованную команду, рабочий каталог, timeout, exit code, stdout/stderr provenance и candidate SHA; secrets в аргументах и выводе должны редактироваться согласно policy.

## Риск

**RISK-001 [P0].** Task risk MUST принимать только `normal` или `critical` и MUST быть назначен до admission. Отсутствующий или неизвестный risk трактуется как `critical`, а не как `normal`.

**RISK-002 [P0].** Stepan MAY автоматически повысить risk до `critical` по policy или найденным признакам, но MUST NOT понизить `critical` до `normal` без явного решения разработчика.

**RISK-003 [P0].** Пока high-risk proof path этапа P2 недоступен, task с risk `critical` MUST быть отклонён на admission с кодом `UNSUPPORTED_RISK_PROFILE`, а не исполнен с ослабленной проверкой.

## Независимое тестирование

**TEST-001 [P2].** Test Worker MUST быть отделён от Implementation Worker по контексту, write boundary и полномочиям.

**TEST-002 [P2].** По умолчанию Test Worker MUST выводить тесты из поведения, acceptance criteria, corner cases и публичных интерфейсов без доступа к реализации. White-box режим требует явной policy и причины.

**TEST-003 [P2].** Test quality gates SHOULD проверять failure paths, нетавтологичные assertions, стабильность, отсутствие неконтролируемых сети/времени и связь критичных тестов с acceptance IDs.

**TEST-004 [P2].** Для `critical` задач proof plan SHOULD включать property/contract tests, mutation testing или обоснованную альтернативу.

## Верификация и завершение

**VER-001 [P0].** Verifier MUST читать governing specification напрямую, а не использовать brief как источник истины.

**VER-002 [P0].** Verifier MUST сравнивать specification, candidate diff, tests и evidence и возвращать только структурированный verdict/findings.

**VER-003 [P0].** Blocking finding MUST указывать конкретный failure scenario, нарушенный invariant/AC, evidence, причину недостаточности существующих tests и минимальный reproduction path.

**VER-004 [P0].** Стиль, вкусовой рефакторинг и недоказанные гипотетические улучшения MUST NOT быть blocking findings.

**VER-005 [P0].** Verdict MUST быть `PASS`, `REWORK`, `BLOCKED` или `OWNER_DECISION` и MUST быть связан с candidate SHA и spec revision.

**VER-006 [P0].** Любое изменение candidate SHA или governing spec revision MUST автоматически делать verdict и approval устаревшими.

**VER-007 [P1].** После `REWORK` отдельный repair worker создаёт новый candidate SHA; полная применимая лестница доказательств выполняется заново.

**VER-008 [P2].** `critical` задачи MUST проходить adversarial verification на replay/idempotency, concurrency, partial failure, authorization, leakage, compatibility и rollback — в применимой части.

**DONE-001 [P0].** Задача может перейти в `done`, только если все обязательные AC имеют evidence, gates зелёные на candidate SHA, diff внутри boundary, blocking findings закрыты, ссылки актуальны, разработчик fast-forward merge-нул approved candidate SHA в target branch, а Controller подтвердил этот Git-факт и выполнил закрывающий переход.

## Evidence, аудит и трассировка

**EVD-001 [P0].** Evidence store MUST сохранять для каждой проверки: evidence ID, task/attempt, candidate SHA, spec revision, тип проверки, команду или tool action, результат, timestamp и provenance.

**EVD-002 [P0].** Verdict MUST содержать mapping каждого обязательного acceptance ID на одно или несколько evidence IDs.

**EVD-003 [P0].** Evidence с другого SHA или spec revision MUST NOT использоваться для closure без явного повторного доказательства применимости.

**EVD-004 [P0].** Controller MUST сохранять machine-produced facts отдельно от свободного текста модели.

**EVD-005 [P1].** Audit log MUST позволять восстановить: кто/какая роль инициировала действие, какой capability был выдан, какой hook сработал, какой переход применён и почему.

**EVD-006 [P1].** Логи и evidence MUST проходить secret redaction и иметь настраиваемую retention policy.

**EVD-007 [P1].** Durable writes SHOULD быть атомарными или транзакционными, а записи — иметь schema version и optimistic concurrency/version field.

**EVD-008 [P0].** Project `.stepan/` MUST хранить компактный evidence manifest, достаточный для проверки applicability: command/tool action, normalized inputs без секретов, outcome/exit code, candidate SHA, spec revision, timestamps, content hashes и краткий deterministic summary.

**EVD-009 [P0].** Полные stdout/stderr, model transcripts и объёмные tool outputs MUST храниться только в `~/.stepan/state/<repository-id>/` по retention policy. Project manifest MUST ссылаться на них по content hash/local artifact ID и явно показывать, доступен ли raw artifact.

## Классы состояния и хранение

**STATE-001 [P0].** Stepan MUST разделять project-persistent artifacts, local run state и disposable temporary data; смешивание этих классов в одном каталоге запрещено.

**STATE-002 [P0].** Project-persistent artifacts MUST храниться в каноническом каталоге `<repository-root>/.stepan/`. Stepan MUST проверить, что resolved path находится внутри корня репозитория и не является перенаправлением наружу через symlink/junction.

**STATE-003 [P0].** Project `.stepan/` MUST хранить состояние, которое должно пережить смену компьютера или клонирование: project configuration/policy, принятые task/decision records, финальные verdicts, компактные evidence manifests и framework learning artifacts. Run checkpoints, leases, locks, raw logs, transcripts и sandbox state MUST NOT туда записываться.

**STATE-004 [P0].** Промежуточное local run state MUST храниться в `~/.stepan/state/<repository-id>/`. Оно MUST быть вне Git worktree, переживать crash/restart и удаляться или архивироваться после terminal state согласно retention policy.

**STATE-005 [P0].** Disposable sandboxes, process streams и временные файлы MUST храниться в `~/.stepan/cache/<repository-id>/` и MAY быть удалены после crash; ни один такой файл не может быть единственным доказательством применённого lifecycle transition.

**STATE-006 [P0].** Изменение project-persistent или local run state MUST быть атомарным, crash-detectable и защищённым от двух одновременно пишущих controller processes посредством repository-scoped lock.

**STATE-007 [P0].** Worker и Verifier MUST NOT получать прямую запись ни в project `.stepan/`, ни в user state directory; записи выполняет только Controller через storage contract.

**STATE-008 [P0].** Candidate diff MUST исключать local run state и temporary data. Изменение project `.stepan/` допускается только отдельной Controller-owned операцией и MUST быть явно показано разработчику.
