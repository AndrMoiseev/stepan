# Отчёт итерации 0: Codex App Server spike

Дата: 2026-08-11

Основание: [спецификация](specification.md), редакция 2

Проверенная версия: `codex-cli 0.147.0`, Windows native/amd64

Итоговый verdict: **SPIKE PASSED WITH CAVEATS**

Ручное ревью contract/report: **APPROVED 2026-08-11**

## Итог

App Server `0.147.0` подтвердил работоспособность stdio JSON-RPC, structured
output, explicit resume и одноразовых command/file approvals. `A07`–`A13`
зафиксировали ограничения встроенной read isolation, narrow grants, lifecycle и
config isolation. Пользователь принял их как управляемые риски: чтение
ограничивается физически отдельным role workspace и OS boundary, процессное
дерево — Windows Job Object с bounded shutdown, конфигурация — отдельным
доверенным профилем и fail-closed inventory. Полное решение и критерии пересмотра
зафиксированы в [ADR 0001](../../adr/0001-codex-app-server-containment.md).

Матрица: **9 PASS, 4 FAIL, 3 BLOCKED**. Ни одного `INCONCLUSIVE`.

## Источники и граница версий

- Pinned baseline: [README](baseline/codex-app-server-0.147.0/README.md),
  [manifest](baseline/codex-app-server-0.147.0/schema-bundle.sha256) SHA-256
  `eb325d394d19f2f8d133203885b3d1c2f74dbc5a176f22078a4f99aae5926faa`.
- Live observations: committed summaries [A01–A16](results/); raw refs и hashes
  сохранены внутри каждого summary. Raw `.stepan/spike` остаётся локальным
  evidence и не включён в Git.
- Текущая [официальная документация Codex App Server](https://learn.chatgpt.com/docs/app-server)
  сверена 2026-08-11. Она подтверждает общую модель, но описывает более новую
  поверхность (`readOnlyAccess`, новые permissions/lifecycle) и не заменяет
  generated schema `0.147.0`.
- Реализованный version-specific contract: [codex-integration-contract.md](codex-integration-contract.md).

## 15 обязательных решений

### 1. Поддерживаемая ОС и точная версия Codex

- Наблюдение: все live-сценарии выполнены на Windows/amd64 с
  `codex-cli 0.147.0`; executable SHA-256
  `935a1911ed2556e4ffcec995f4886ac2ac425863ba26fed264df62e30272ad9d`.
- Выбранный вариант: поддерживать только эту точную пару ОС/версии как spike
  baseline.
- Отклонено: обещание совместимости с current/latest Codex, Linux или macOS по
  документации либо cross-build.
- Последствия: любая смена версии или ОС требует schema diff и повторных live
  сценариев.
- Evidence: `results/A01.json` SHA-256
  `36af387b9ea361da335fb9ee22794e6e2dc0e62d9e3eaa479651a7b3ba060296`,
  `results/A15.json` SHA-256
  `4007482eb9d15d592614e5733eb350ca56052c7f151d208fdc083139a40daf26`.

### 2. Пригодность experimental App Server для pinned-version MVP

- Наблюдение: transport, resume и write approvals работают, но четыре
  обязательные safety/lifecycle области провалены.
- Выбранный вариант: **SPIKE PASSED WITH CAVEATS**; App Server становится
  transport итерации 1 вместе с обязательными внешними containment gates из
  ADR 0001.
- Отклонено: broad permissions, experimental API и принятие `readOnly` или
  direct process kill за доказанную границу безопасности.
- Последствия: iteration 1 сначала реализует role-workspace, Job Object и config
  preflight; без них соответствующий run завершается fail-closed.
- Evidence: `results/A07.json`–`A13.json`; точные hashes приведены в матрице.

### 3. Точный initialize/thread/turn contract

- Наблюдение: live подтверждён порядок `initialize` → `initialized` →
  `configRequirements/read` → `thread/start|resume` → `turn/start` →
  `turn/completed`; experimental capability не отправлялась.
- Выбранный вариант: контрактировать только wire-значения `0.147.0`, включая
  `on-request`, `readOnly` и `read-only`, как описано в integration contract.
- Отклонено: current-doc wire-значения, permissive parsing противоречивых
  terminal messages и вывод IDs друг из друга.
- Последствия: version mismatch должен fail closed.
- Evidence: `results/A01.json`; raw
  `.stepan/spike/task4-live-20260810/a01-final-evidence/stdout.jsonl` SHA-256
  `99b0a34448b34b2a16b3a034f45bab267d2f6a4b0d0305467a355f560249865d`.

### 4. Извлечение structured final output

- Наблюдение: authoritative object получен из
  `turn/completed.params.turn.items[].agentMessage[phase=final_answer].text` и
  прошёл typed validation.
- Выбранный вариант: ровно один final message, strict JSON decode, запрет
  unknown fields/trailing data, exact nonce.
- Отклонено: regex, последняя JSON-подстрока, свободный agent text.
- Последствия: provider/schema drift становится явным
  `structured_output_failure`.
- Evidence: `results/A01.json`; raw result
  `.stepan/spike/task4-live-20260810/a01-final-evidence/result.json` SHA-256
  `c015acf0ec94627411f2f343653ce368d009e50d96f482db8538be7e4afee423`.

### 5. Resume и restart behavior

- Наблюдение: после restart новый App Server возобновил exact thread ID,
  создал новый turn ID и вспомнил nonce, отсутствовавший в resume prompt.
- Выбранный вариант: сохранять thread ID и выполнять явный `thread/resume`;
  оборванные approvals закрывать fail-closed, не «восстанавливать» stdio request.
- Отклонено: новый thread с реконструированным prompt и повторная отправка
  старого request ID.
- Последствия: resume conversation подтверждён; mid-approval recovery требует
  отдельного явного retry.
- Evidence: `results/A02.json`; raw
  `.stepan/spike/task4-live-20260810/a02-final-evidence/result.json` SHA-256
  `8e7e4c6fa22e0cddd17fe4a1eb842107783309da7386bc0d46d912e66d3107a4`.

### 6. Correlation и lifecycle server requests

- Наблюдение: opaque string/int64 request IDs и независимые thread/turn/item IDs
  корректно коррелируются; duplicate/orphan responses и unresolved terminal
  отклоняются fake/replay тестами.
- Выбранный вариант: регистрировать pending до durable decision и разрешать
  request ровно один раз.
- Отклонено: correlation по порядку, command text или производным IDs.
- Последствия: неизвестный/противоречивый lifecycle становится protocol failure.
- Evidence: `results/A03.json`–`A06.json`,
  `internal/codexapp/transport_test.go`, `internal/codexapp/approvals_test.go`.

### 7. Policy accept/decline и делегирование оператору

- Наблюдение: `A03` выполнил write после recorded accept, `A04` не выполнил
  запрещённую операцию после decline, `A05` разделил allowed/protected changes,
  `A06` durable сохранил operator decision и отправил его один раз.
- Выбранный вариант: exact allowlist, одноразовые accept/decline/cancel,
  operator для явно перечисленных классов.
- Отклонено: `acceptForSession`, amendments, shell parsing и implicit allow.
- Последствия: меньше автоматизации, но нет неявного расширения полномочий.
- Evidence: raw approval logs из `A03`–`A06`, например
  `.stepan/spike/task5-live-20260811/a06-73b9f4/evidence/approvals.jsonl` SHA-256
  `a429c5ef97284e30c534d99bfa42af34de1d14cf2ef10aec6fe9691bec640e99`.

### 8. Restricted read и отсутствие source leakage

- Наблюдение: `readOnly` `0.147.0` не ограничил чтение intended roots; direct и
  shell reads раскрыли source canary четырежды в двух model-visible artifacts.
- Выбранный вариант: считать гипотезу встроенной изоляции опровергнутой и
  использовать физически отдельный workspace без source и с OS-level boundary.
- Отклонено: считать read-only ограничением чтения или полагаться на prompt.
- Последствия: blind test-author нельзя запускать рядом с production source.
- Evidence: `results/A07.json`, `results/A08.json`; raw normalized events
  `.stepan/spike/task6-live-20260811/a07-a08-final3/evidence/normalized-events.jsonl`
  SHA-256 `5360b165ad6882c7e434af27d68f575f4508f03e280ac14fab863cdcca4db18b`.

### 9. Turn-scoped read grant без experimental API

- Наблюдение: permission response schema допускает `scope="turn"`, но stable
  request tool помечен under development и disabled; `TurnStartParams` не имеет
  restricted readable roots.
- Выбранный вариант: dynamic grant не выдавать; после operator approval
  материализовать конкретный input в новом изолированном workspace и начать
  новый turn.
- Отклонено: broad/session grant и experimental fallback.
- Последствия: `A09`/`A10` нельзя выполнить на обязательном stable path.
- Evidence: `results/A09.json`, `results/A10.json`; schema observation
  `.stepan/spike/task6-live-20260811/contract-observation.json` SHA-256
  `bf01a3a8a7b694e5ddc58e5330ad0f22cb73e96124e75fb049ab82bde4b56a47`.

### 10. Configuration isolation и managed requirements

- Наблюдение: auth из existing `CODEX_HOME` работает и requirements/instruction
  sources читаются, но user layer загрузил 3 plugin hooks, 11 enabled plugins и
  3 MCP servers даже в attempted isolated run; `A14` подтвердил project/user
  влияние в inherited mode.
- Выбранный вариант: отдельный доверенный Codex profile, штатная авторизация и
  fail-closed allowlist effective config/hooks/plugins/MCP перед turn.
- Отклонено: называть очищенный environment изоляцией при сохранённом
  `CODEX_HOME`, копировать credentials или игнорировать effective config.
- Последствия: clean config/auth preflight становится первым обязательным gate
  итерации 1.
- Evidence: `results/A13.json`, `results/A14.json`; raw
  `.stepan/spike/task7-live-20260811/a13-isolated-escalated/raw-summary.json`
  SHA-256 `e1f2064581af5a4e796d8122cd89096224a46867957880138a1c3c70bd775aae`.

### 11. Timeout, cancel и process tree

- Наблюдение: App Server path не реализует bounded `turn/interrupt`, execution/
  approval timeout, operator cancel и Windows Job Object; отсутствие потомков не
  доказано. Legacy Job Object test прошёл одиночные случаи, но stress 20 не прошёл.
- Выбранный вариант: внешний Windows Job Object; сначала best-effort
  `turn/interrupt`, затем bounded grace и принудительное закрытие всего дерева.
- Отклонено: переносить положительные legacy assertions на новый процессный путь
  или считать `Process.Kill` достаточным доказательством дерева.
- Последствия: controller пока не гарантирует bounded shutdown и cleanup.
- Evidence: `results/A12.json`; raw
  `.stepan/spike/task7-live-20260811/a12-audit/focused-stress.txt` SHA-256
  `ce29e219f5a4c5fe63363654e592538f35a61a101b325ad59d4471cf430ba669`.

### 12. Durable approval state и recovery pending request

- Наблюдение: pending/decision/sent записываются до соответствующих действий;
  operator response отправляется один раз. После connection loss локальное
  pending state закрывается fail-closed; protocol recovery старого request не
  подтверждён.
- Выбранный вариант: durable audit + fail-closed, затем explicit resume/retry.
- Отклонено: at-least-once resend старого response и предположение, что stdio
  request пережил restart.
- Последствия: нет silent replay side effects, но нет прозрачного mid-turn resume.
- Evidence: `results/A06.json`, `results/A12.json`,
  `internal/codexapp/approvals_test.go`.

### 13. Candidate snapshot

- Наблюдение: временный Git index даёт HEAD/tree identity, видит tracked,
  untracked, deletion, rename, staged+unstaged, Unicode и same-size/timestamp
  changes, не меняя настоящий index; approvals фиксируют before/after snapshots.
- Выбранный вариант: оставить Git-native temporary-index snapshot.
- Отклонено: content walk, изменение настоящего index и собственный hash tree.
- Последствия: snapshot минимален и повторяет Git semantics; submodule/symlink/
  file-mode риски остаются платформенными ограничениями.
- Evidence: `results/A03.json`, `results/A05.json`,
  `internal/gitsnapshot/snapshot_test.go`.

### 14. Windows-ограничения и кроссплатформенные риски

- Наблюдение: direct argv/JSON paths с пробелами, кириллицей, `&`, `[]` прошли;
  shell escaping не потребовался. Runtime evidence других ОС нет.
- Выбранный вариант: Windows-only; общими оставить parser/state/policy/snapshot,
  OS-specific lifecycle — отдельным.
- Отклонено: считать cross-build runtime evidence.
- Последствия: Linux/macOS требуют проверки signals/process groups, atomic
  replace, symlinks/modes, config layering и sandbox enforcement.
- Evidence: `results/A15.json`; raw manifest
  `.stepan/spike/task7-live-20260811/a15-paths/Путь с пробелом & [скобки]/Артефакты & [результат]/evidence/manifest.json`
  SHA-256 `afa1f39349b97d29b9d5afd6439b30bf2a686ebede04cd492a75963bc6a4472c`.

### 15. Судьба legacy `codex exec` probe и evidence

- Наблюдение: legacy `C06` (разрешённая запись) — FAIL, `C07` (внешняя запись
  запрещена) — PASS; transport/final output были валидны. Legacy lifecycle код
  остаётся полезным как диагностическая реализация, но stress process-tree
  нестабилен и односторонний `codex exec` не даёт нужного approval loop.
- Выбранный вариант: сохранить `cmd/codex-probe`, `internal/codexexec`, fixtures,
  script и raw observations как legacy evidence; не использовать как основной
  transport и не удалять до решения App Server lifecycle gap после review.
- Отклонено: удалить сейчас либо вернуть `codex exec` основным transport.
- Последствия: временно остаётся дублирующий probe; удалить его можно отдельным
  change, когда replacement покрывает timeout/cancel/tree и evidence принято.
- Evidence: `.stepan/manual/20260810-204353/manual-result.json` SHA-256
  `16d94fcfa950469763188206da497fc04548ad48da2bb623f57e52aef929baaa`,
  `results/A12.json`, `scripts/test-codex-exec-permissions.ps1`.

## Матрица A01–A16

| ID | Итог | Краткий вывод | Committed summary SHA-256 | Primary raw evidence |
|---|---|---|---|---|
| A01 | PASS | handshake, turn, typed final | [A01](results/A01.json) `36af387b9ea361da335fb9ee22794e6e2dc0e62d9e3eaa479651a7b3ba060296` | `.stepan/spike/task4-live-20260810/a01-final-evidence/result.json` `c015acf0ec94627411f2f343653ce368d009e50d96f482db8538be7e4afee423` |
| A02 | PASS | exact resume после restart | [A02](results/A02.json) `4b12acdb0684c76b1129d144cc53432897fe784dcfa792bc8d33fbfe197e9502` | `.stepan/spike/task4-live-20260810/a02-final-evidence/result.json` `8e7e4c6fa22e0cddd17fe4a1eb842107783309da7386bc0d46d912e66d3107a4` |
| A03 | PASS | write только после accept | [A03](results/A03.json) `e2512e16de683f569bb3afeedc338ad7d2d3e73110e208ec67920ca40f7f2331` | `.stepan/spike/task5-live-20260811/a03-c8e42d/evidence/approvals.jsonl` `aee4bfd41f6a849ce755a038d7aca5f0d55a09c892d1d4936d8bc1ae6e8eec4f` |
| A04 | PASS | external write declined | [A04](results/A04.json) `f4b8b20557a9e03331eebd79940e727333c7cda1548277e30ee8ab32ed21724d` | `.stepan/spike/task5-live-20260811/a04-51de8a/evidence/result.json` `461a97c2236c94b2d6736c4df996a25c161ef7e90e19b3bff9f02ee4b75104ea` |
| A05 | PASS | allowed/protected paths разделены | [A05](results/A05.json) `8f4a06ca9e10cc637b870f91a3032357b10ff894bf49117f7ecfff901ea2effb` | `.stepan/spike/task5-live-20260811/a05-4e1c93/evidence/result.json` `b8f523ffe609fab607dbfa0442d764d5e65516467a7a5b27c80061d8c5c6c924` |
| A06 | PASS | durable operator decision one-shot | [A06](results/A06.json) `56e68981a5edd0fde14e3d07792e6c3ac6beefc271b699c76840f4e0be818a99` | `.stepan/spike/task5-live-20260811/a06-73b9f4/evidence/approvals.jsonl` `a429c5ef97284e30c534d99bfa42af34de1d14cf2ef10aec6fe9691bec640e99` |
| A07 | FAIL | intended readable roots не enforced | [A07](results/A07.json) `c8e9d6a692bc87fadc8a8c10453869070788e50faad9d6644c42e7fcc74c7780` | `.stepan/spike/task6-live-20260811/a07-a08-final3/raw-summary.json` `13abe8fd33b472732a302dd4b258a70fdc29fa1dfc7c762dc7f6fda5ecebfb24` |
| A08 | FAIL | source canary leaked | [A08](results/A08.json) `c184ced36e5fd9a09caca815cef8197059385f567313135da18fce8481aba09f` | `.stepan/spike/task6-live-20260811/a07-a08-final3/evidence/normalized-events.jsonl` `5360b165ad6882c7e434af27d68f575f4508f03e280ac14fab863cdcca4db18b` |
| A09 | BLOCKED | stable permission request недоступен | [A09](results/A09.json) `1edaff8e6ddd85034e63718b6ecc6b3f7303b12347c1d2145be1db27c300d8d3` | `.stepan/spike/task6-live-20260811/contract-observation.json` `bf01a3a8a7b694e5ddc58e5330ad0f22cb73e96124e75fb049ab82bde4b56a47` |
| A10 | BLOCKED | narrow stable turn grant недоступен | [A10](results/A10.json) `f847892e3702e113e30c150d98f54dcb5d45a4c80283887fe722f1a91bb25e82` | `.stepan/spike/task6-live-20260811/contract-observation.json` `bf01a3a8a7b694e5ddc58e5330ad0f22cb73e96124e75fb049ab82bde4b56a47` |
| A11 | BLOCKED | blind author не изолирован | [A11](results/A11.json) `1d379c8629e916863a644ad8d7219a561f4480fac43cbdd23d4642977f74b67b` | `.stepan/spike/task6-live-20260811/a07-a08-final3/raw-summary.json` `13abe8fd33b472732a302dd4b258a70fdc29fa1dfc7c762dc7f6fda5ecebfb24` |
| A12 | FAIL | App Server cancel/tree gap | [A12](results/A12.json) `3847772251b7f52a1452577770c74c4aa0d72a0f47c5207235f9a0a665c08ce5` | `.stepan/spike/task7-live-20260811/a12-audit/focused-stress.txt` `ce29e219f5a4c5fe63363654e592538f35a61a101b325ad59d4471cf430ba669` |
| A13 | FAIL | user config/plugins/hooks/MCP влияют | [A13](results/A13.json) `98f708b4f45a50089a5aaf0d696c32ab6851d0e73cac46a272c8ea9ca54e38ba` | `.stepan/spike/task7-live-20260811/a13-isolated-escalated/raw-summary.json` `e1f2064581af5a4e796d8122cd89096224a46867957880138a1c3c70bd775aae` |
| A14 | PASS | inherited influence измерено | [A14](results/A14.json) `7f1f9ba7a2fbdd792cd0c54e99015a049b92f72016cb587356c8155081b689d9` | `.stepan/spike/task7-live-20260811/a14-inherited/raw-summary.json` `243e458897ec0c55bcd86a453d60fc5e4d13874ab7d5fd94e19c7b01f9c47a94` |
| A15 | PASS | Windows special paths/UTF-8 | [A15](results/A15.json) `4007482eb9d15d592614e5733eb350ca56052c7f151d208fdc083139a40daf26` | `.stepan/spike/task7-live-20260811/post-process-check.json` `28105974a172bc61ae97a3eece4a0d659255dfadff3e81ceb378ea8160005254` |
| A16 | PASS | stderr/process/protocol классифицированы | [A16](results/A16.json) `9c91f609d50e299597d4f8253580425e089e350e0ac9e5672b57e84ee1a37a29` | `.stepan/spike/task4-live-20260810/a16-process-evidence/result.json` `4dbcf8a4012ae0cf544ccdcd22b0858d4743d467924aafae8ac63bdea1ab2af8` |

## Сверка Definition of Done

| Критерий спецификации, строка за строкой | Статус | Основание |
|---|---|---|
| `go test ./...` без live Codex | PASS | fake/replay subprocess only |
| fake App Server: framing, approvals, malformed, timeout, tree | FAIL | framing/approvals/malformed есть; App Server timeout/tree path отсутствует (`A12`) |
| все A01–A16 имеют однозначный итог | PASS | 9 PASS / 4 FAIL / 3 BLOCKED |
| основной путь без INCONCLUSIVE | PASS | INCONCLUSIVE нет |
| live handshake, structured output, resume | PASS | A01, A02 |
| allowed operation только после recorded decision | PASS | A03 |
| forbidden/unanswered operation не выполняется | PASS | A04 и fail-closed tests |
| pending operator approval продолжается один раз | PASS | A06 |
| restricted read подтверждён filesystem evidence | FAIL | A07 |
| source canary отсутствует в model-visible output/artifacts | FAIL | A08: 4 occurrences |
| A10 narrow turn-scoped stable grant | FAIL | A09/A10 BLOCKED |
| blind test-author + separate verifier | FAIL | A11 BLOCKED |
| timeout/cancel не оставляет App Server/children | FAIL | A12 |
| effective config/requirements/instructions соответствуют policy | FAIL | A13 обнаружил undeclared influence |
| candidate snapshot стабилен и не меняет index | PASS | snapshot regression + A03–A06 |
| contract/report прошли ручное ревью | PASS | пользователь принял результат 2026-08-11 и ADR 0001 |
| Git не содержит credentials/live `.stepan` artifacts | PASS | tracked-tree scan; `.stepan/` ignored |
| roadmap итерации 1 не зависит от unknown behavior | PASS | gaps закрываются явными gates ADR 0001; roadmap синхронизирован |

Строгие критерии спецификации выявили ожидаемые gaps. Пользователь принял их как
явные ограничения MVP с внешними compensating controls из ADR 0001; поэтому
spike завершён с замечаниями и переход к итерации 1 разрешён.

## Сработавшие условия и принятое решение

| Условие | Результат |
|---|---|
| schema нельзя сгенерировать/согласовать | не сработало: bundle и live A01 согласованы |
| нельзя связать request/decision/item/turn/thread | не сработало: A03–A06 |
| allowed write не выполняется после accept | не сработало: A03 |
| запрещённая операция выполняется до/после решения или вне scope | **сработало: A08 прочитал source вне intended scope без approval** |
| turn success с unresolved approval | не сработало: production state machine отклоняет |
| operator decision нельзя durable сохранить/one-shot отправить | не сработало: A06 |
| restricted read допускает source canary | **сработало: A07/A08** |
| запрещённое содержимое попадает в output/artifacts | **сработало: A08** |
| narrow read требует broad/session либо experimental | **сработало: A09/A10** |
| user/project/managed config незаметно меняет contract | **сработало: A13; A14 показывает механизм влияния** |
| structured output требует эвристики | не сработало: A01 |
| exact resume не работает/меняет context | не сработало: A02 |
| App Server/children остаются после timeout/cancel | **не доказано обратное; A12 требует внешнего Job Object gate** |
| candidate snapshot меняет index/не видит changes | не сработало: regression tests и A03–A06 |

Условия действительно сработали и результаты сценариев не изменены. После
ручного review они классифицированы как принятые риски, потому что ADR 0001
задаёт независимые OS/workspace/config controls и fail-closed поведение. Любое
нарушение этих controls снова блокирует конкретный run и требует пересмотра ADR.

## Fixtures и sanitization

Оставлены ровно четыре App Server replay fixtures:

- `internal/codexapp/testdata/success.jsonl`;
- `internal/codexapp/testdata/approval.jsonl`;
- `internal/codexapp/testdata/process-failure.jsonl`;
- `internal/codexapp/testdata/protocol-failure.jsonl`.

Они содержат только synthetic IDs/nonce и placeholders. Один тест проверяет их
фиксированные SHA-256 канонического LF-текста, отсутствие canary/credential markers, локальных Windows,
UNC, `/Users` и `/home` paths, затем проигрывает каждый файл через production
`Transport`, `runProtocol`, approval manager и production failure classifier.
Старый отдельный approval-only fixture удалён как дубликат класса.

## Принятое решение и follow-up

1. Пользователь принял spike с замечаниями; compensating controls и критерии
   пересмотра записаны в [ADR 0001](../../adr/0001-codex-app-server-containment.md).
2. `docs/product-brief.md` и `docs/implementation-roadmap.md` синхронизированы с
   post-approval решением.
3. Отдельное решение о физическом удалении legacy `codexexec` принимается только
   после появления и проверки replacement lifecycle; сейчас evidence сохранён.
