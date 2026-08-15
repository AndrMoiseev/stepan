# Reality review: Architecture Spine — Stepan

Дата проверки: 2026-08-13
Ревьюер: configured reviewer 1 — committed decisions and technology reality

## Verdict

**CHANGES REQUIRED.** Основная архитектурная линия согласуется с product brief, roadmap и текущим кодом, но spine пока нельзя переводить в `final`: общий контракт требует JSON Schema 2020-12, тогда как официально документированный Claude Code принимает draft-07; кроме того, `verified` для Codex употреблён шире имеющегося evidence, а отложенная политика расширений фактически получила default в AD-8.

## Findings

### R1 — BLOCKER: общий schema-контракт несовместим с документированным Claude Code

**Где:** AD-5 и Stack.

Spine требует от каждого поддерживаемого адаптера полный общий контракт и передаёт `output JSON Schema 2020-12`. При этом официальная документация Claude Code прямо указывает, что structured-output validator использует **JSON Schema draft-07** и отвергает схемы с более новой декларацией. Это не просто непроверенная деталь: текущие схемы Stepan действительно объявляют 2020-12 (`internal/specflow/contracts.go`, `internal/codexapp/client.go`). Следовательно, Claude adapter не сможет выполнить заявленный полный контракт без ограничения или преобразования dialect.

Источник: [Claude Code structured outputs](https://code.claude.com/docs/en/agent-sdk/structured-outputs) (раздел Type-safe schemas: validator draft-07, newer versions rejected).

**Clear fix:** заменить в общем runner-контракте `JSON Schema 2020-12` на явно определённый portable subset draft-07, достаточный для текущих схем, либо сделать dialect/portable-schema contract отдельным принятым решением с обязательным lossless conformance для каждого адаптера. Самый короткий совместимый вариант — draft-07 и миграция `$schema` в текущих role schemas; до live Codex conformance не называть это проверенным.

### R2 — HIGH: `verified 0.147.0 on Windows/amd64` означает больше, чем доказывает repository evidence

**Где:** Stack, пояснение после таблицы, AD-12.

Repository evidence подтверждает live App Server transport/structured output/resume для `codex-cli 0.147.0` на Windows/amd64, но тот же отчёт оставляет process-tree lifecycle, source-blind isolation и config isolation как `BLOCKED`/неподтверждённые сценарии (`docs/specs/iteration-0/report.md`, A11–A13). ADR 0001 разрешает дальнейшую работу только при добавлении внешних gates. Общей `agent.Runner` conformance suite и живой матрицы из AD-12 в репозитории пока нет.

Поэтому значение `verified` в Stack противоречит собственному определению AD-12, если читатель понимает его как проверку всего target execution envelope.

**Clear fix:** назвать `0.147.0 / Windows/amd64` **observed/accepted transport baseline**, а `target Runner conformance` оставить pending до живой матрицы. Minimum `0.147.0` можно сохранить как принятое target-решение, но не как доказательство полной совместимости.

### R3 — HIGH: отложенная политика расширений стала обязательным Codex default

**Где:** последнее предложение AD-8 против Deferred Decisions и memlog.

В ходе elicitation явно зафиксировано: варианты `clean profile`, `allowlisted skills with hooks/MCP disabled`, `inherited profile` только перечисляются; default будет выбран при проектировании функции. Однако AD-8 предписывает target-пути Codex отдельный trusted profile и fail-closed inventory/allowlist hooks, plugins, MCP и instruction sources. Это фактически выбирает один default, пусть и со ссылкой на baseline ADR 0001.

Текущий код/ADR действительно использует этот baseline, поэтому исторически утверждение верно; как target invariant оно конфликтует с последним пользовательским решением.

**Clear fix:** отделить переходный baseline от target: «до миграции существующий Codex path регулируется ADR 0001; target extension policy не выбрана». Саму allowlist-политику оставить только одним из вариантов в Deferred Decisions.

Актуальные CLI-возможности не снимают необходимость решения: Codex документирует `--ignore-user-config`, а Claude Code документирует `--bare`/`--safe-mode`; при этом `--bare` отключает и skills, и hooks/plugins/MCP, то есть не доказывает режим «разрешить skills, запретить остальное». Источники: [Codex developer commands](https://developers.openai.com/codex/cli/reference/), [Claude Code headless mode](https://code.claude.com/docs/en/headless), [Claude Code CLI reference](https://code.claude.com/docs/en/cli-usage).

### R4 — HIGH: правило commit/replay журнала не позволяет однозначно отличить оборванный хвост

**Где:** AD-2.

`append + fsync` описывает момент успешного возврата writer, но формат не задаёт observable framing для replay. Без правила о завершающем newline/длине/checksum replay не может отличить неполную последнюю запись от повреждённой committed-записи, хотя spine требует первую игнорировать, а на второй fail-closed. Полностью записанная, но ещё не `fsync`-нутая строка также может пережить crash.

**Clear fix:** определить минимальное framing-правило: одна canonical JSON-запись + `\n`; writer считает append успешным только после полного write, flush и file `fsync`; replay игнорирует только финальные bytes без `\n`, а любой newline-terminated invalid JSON, gap или unknown type/version останавливает run. Если различие между survived-before-fsync и committed принципиально, нужен commit marker/length+checksum; сейчас это не требуется остальными решениями.

### R5 — MEDIUM: dependency rule расходится с собственной диаграммой

**Где:** абзац после диаграммы и AD-1.

Фраза «конкретные реализации связывает только `cmd/stepan`» не соответствует диаграмме: `agent/codex` и `agent/claude` напрямую зависят от concrete `processcontrol`, а `execution` — от concrete `gitrepo`. Это может быть правильной простой архитектурой без лишних интерфейсов, но формулировка обещает другое.

**Clear fix:** сузить утверждение: только выбор и injection concrete `agent.Runner` выполняет `cmd/stepan`; `gitrepo`, `runstore` и build-tagged `processcontrol` остаются внутренними concrete packages с узким API.

### R6 — MEDIUM: macOS process group не равен гарантированному process tree

**Где:** AD-9.

POSIX process group/signals покрывают процессы, сохранившие группу, но сами по себе не гарантируют завершение потомка, который создал новую session/process group. Поэтому одновременно фиксировать реализацию «macOS POSIX process group/signals» и безусловный результат «завершает всё дерево turn» слишком сильно. Указанный live lifecycle gate снижает риск, но не превращает process group в security boundary.

**Clear fix:** назвать это lifecycle containment, не security isolation; поддержку macOS объявлять только для конкретной матрицы agent/version после child-escape/lifecycle tests. Если тест показывает detached descendants, текущего механизма недостаточно и platform остаётся unsupported.

### R7 — LOW: версии Go/huh корректны как repository baseline, но не как актуальные рекомендации

**Где:** Stack.

- `go.mod` действительно содержит Go `1.26.5`, но Go `1.26.6` с security fixes опубликован 2026-08-13: [Go release history](https://go.dev/doc/devel/release).
- `go.mod` действительно содержит `charm.land/huh/v2 v2.0.3`; pkg.go.dev подтверждает версию, но помечает её не последней: [huh v2 package](https://pkg.go.dev/charm.land/huh/v2).

Это не делает spine неверным, если таблица описывает текущий repository baseline. Однако patch-версия Go и UI dependency не являются архитектурными pins.

**Clear fix:** подписать их как `current go.mod`, а upgrade policy оставить обычному dependency management. Не обновлять зависимости только ради architecture review; отдельно оценить Go 1.26.6 из-за security fixes.

## Claims checked and accepted

- Текущий code baseline совпадает с ADR 0002: modular monolith, `cmd/stepan -> internal/specflow -> codexapp/gitsnapshot/processjob`, точный отказ от Codex версий кроме `0.147.0`, Windows/amd64 preflight, Windows Job Object и fake/replay tests.
- Product invariants AD-4 и AD-15 (explicit spec/plan approval, clean tree, sequential execution, Stepan-owned commits, no push, final verification, `HEAD OID + tree OID`) соответствуют `docs/product-brief.md` и `docs/implementation-roadmap.md`.
- Claude Code официально документирует non-interactive `-p`, JSON/stream-json output, validated JSON Schema output, resume by ID/name, strict MCP config and bare/safe modes. Сам spine корректно оставляет Claude minimum/version/platform `pending live conformance`; документация не заменяет проверку.
- Go `1.26.5` и huh `v2.0.3` не выдуманы: это точные значения текущего `go.mod`.
- `Codex CLI >= minimum + warning above last verified` — принятое target-решение, а не свойство текущего кода; вступительный переходный абзац это в целом объясняет. После исправления R2 различие станет однозначным.
- Отсутствие daemon/database/public plugin ABI, один writer, последовательный current-checkout workspace и deferred parallel worktrees согласуются с product scope и elicited decisions.

## Minimal fix set before final

1. Сделать общий output schema contract совместимым с Claude draft-07 или явно portable/lossless.
2. Переименовать Codex `verified` в transport baseline; target conformance оставить pending.
3. Удалить выбранный extension default из AD-8, сохранив ADR 0001 только как transition rule.
4. Зафиксировать newline framing оборванного JSONL tail.
5. Уточнить composition-root и macOS containment формулировки.
6. Пометить Go/huh как текущие значения `go.mod`, не архитектурные pins.

## Resolution check

Повторная проверка: 2026-08-13.

**PASS.** Все clear fixes закрыты без новых противоречий:

- common output contract ограничен версионированным Stepan-подмножеством draft-07;
- Codex `0.147.0` назван transport baseline, а не `verified` для target Runner;
- extension policy снова явно deferred, ADR 0001 оставлен только переходным baseline;
- JSONL получил LF framing, однозначное правило torn tail и exclusive writer lock;
- composition root описан совместимо с capability imports;
- macOS process-group ограничение и live support gate сформулированы явно;
- Go и huh помечены как текущие значения `go.mod`, не архитектурные pins.
