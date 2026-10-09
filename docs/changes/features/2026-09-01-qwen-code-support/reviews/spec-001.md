---
review_id: SPEC-REVIEW-001
stage: spec
status: awaiting_decisions
created_at: 2026-09-02T21:37:00.058997+03:00
updated_at: 2026-09-02T22:16:21.8504+03:00
provider: "codex"
model: "default"
started_revision: a4ab26e6bb3284a5a0b0cbfe6ccb63ceb5b60e0bcb11d50f07786cadc0b964be
accepted_for_revision: a4ab26e6bb3284a5a0b0cbfe6ccb63ceb5b60e0bcb11d50f07786cadc0b964be
upstream_started:
  intent.md: c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c
upstream_accepted:
  intent.md: c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c
attempts: 2
---

# Ревью спецификации

## SPEC-F-001 — Официальный Qwen не может получить требуемый внешний корень артефакта

Severity: blocker
Status: open
Problem: REQ-007 and DEC-007 require the external artifact root to be supplied per ACP session through `additionalDirectories`, while the current official Qwen stdio ACP implementation neither advertises `sessionCapabilities.additionalDirectories` nor consumes that request field: its `initialize` response advertises only `list` and `resume`, and `newSession` reads only `cwd` and `mcpServers` ([Qwen `acpAgent.ts`](https://raw.githubusercontent.com/QwenLM/qwen-code/main/packages/cli/src/acp-integration/acpAgent.ts)). The ACP contract requires clients to gate `additionalDirectories` on the advertised capability ([ACP additional-directories contract](https://agentclientprotocol.com/rfds/additional-directories)). Because the artifact root must remain outside the Git root and process-wide roots/fallbacks are explicitly prohibited, the specified one-process topology cannot provide the only writable location to the official Qwen implementation, so the core observable outcome is not currently feasible.
Location: REQ-007, REQ-018, DEC-007, AC-002, AC-004
Traces: REQ-007, REQ-018, DEC-007, AC-002, AC-004
Recommendation: Выбрать и зафиксировать реализуемую границу совместимости: либо сделать объявленную и фактически поддержанную ACP capability `additionalDirectories` обязательным условием выпуска и явно признать текущую опубликованную версию официального Qwen несовместимой до появления этой capability, либо пересмотреть топологию и модель доступа — например, использовать отдельный process на thread с контролируемым startup root или доступный для записи инструмент под управлением Stepan — сохранив инвариант внешнего artifact root.
Decision: fix
Decided-by: user
Rationale: Риск общей видимости подключённых на уровне процесса дополнительных каталогов принимается; автор должен привести архитектуру и требования к реализуемой схеме глобального подключения каталогов либо соответственно изменить топологию.

## SPEC-F-002 — Не определено восстановление ответа ассистента из ACP-событий

Severity: major
Status: dismissed
Problem: REQ-009 says to collect the final assistant response of `session/prompt`, but an ACP `session/prompt` response carries completion/stop information while assistant text is streamed through `session/update` `agent_message_chunk` notifications ([ACP overview](https://agentclientprotocol.com/protocol/v2/overview)); Qwen emits both model text and some discrete diagnostic/status text through that same update type ([Qwen `Session.ts`](https://raw.githubusercontent.com/QwenLM/qwen-code/main/packages/cli/src/acp-integration/session/Session.ts)). The specification does not define which chunks constitute the candidate envelope, their ordering and boundaries, treatment of thought/non-text/discrete messages, or behavior on retries and chunks arriving around the terminal response. Consequently, two conforming implementations can derive different JSON payloads, and AC-005 does not test fragmented or mixed assistant updates.
Location: REQ-009, REQ-010, DEC-003, AC-005, AC-006
Traces: REQ-009, REQ-010, DEC-003, AC-005, AC-006
Recommendation: Определить детерминированный per-prompt автомат извлечения: перечислить принимаемые варианты ACP update и типы content, сохранять wire order, задать границы сообщений и правила retry/reset, определить terminal barrier и обработку late/duplicate events, а также завершаться fail-closed, если невозможно восстановить единственный однозначный JSON-текст. Расширить AC-005 и AC-006 сценариями с фрагментированными chunks, чередованием thought/status, несколькими assistant messages и поздними updates.
Decision: dismiss
Decided-by: user
Rationale: Риск принимается на текущем этапе; если проблема проявится при тестировании, способ исправления будет выбран на основании наблюдаемого поведения и собранной фактуры.

## SPEC-F-003 — Для изолированного запуска и сторонних CLI нет совместимого контракта

Severity: major
Status: dismissed
Problem: REQ-011 requires an exact five-tool inventory and disables ambient configuration, built-in subagents, shell, web, MCP, hooks, extensions, skills, memory, and background work before any model turn, but the documented launch is only `qwen --acp` and REQ-018 does not name a standard or Qwen-specific attestation mechanism. Official Qwen `--safe-mode` still loads built-in subagents and requires additional tool exclusions for the stated inventory ([Qwen headless configuration](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/headless.md)); standard ACP initialize capabilities do not enumerate model-visible tools. An arbitrary `--agent-cli` supplies only an executable path, so a protocol-compatible third-party CLI with different isolation flags or no Qwen status extensions has no defined way to receive or prove the required profile. This conflicts with REQ-003/DEC-009/AC-014, which make protocol/tool behavior—not branding or an undocumented Qwen CLI surface—the compatibility boundary.
Location: REQ-003, REQ-004, REQ-011, REQ-018, DEC-009, AC-007, AC-014
Traces: REQ-003, REQ-004, REQ-011, REQ-018, DEC-009, AC-007, AC-014
Recommendation: Определить полный startup contract и канал подтверждения. Для официального CLI зафиксировать обязательные argv/environment, которые включают safe mode, default one-shot approvals и точные исключения инструментов, а также конкретный preflight-метод аттестации effective inventory. Для сторонних CLI следует либо определить версионируемый provider-neutral или Qwen-compatible профиль запуска и аттестации, который они обязаны реализовать, либо добавить явный задаваемый пользователем launch profile, либо сузить обещание с protocol-compatible CLI до executable, совместимых одновременно с ACP wire contract и документированным Qwen launch profile.
Decision: dismiss
Decided-by: user
Rationale: Риск несовместимости принимается; пользователь обязан самостоятельно убедиться, что выбранный сторонний CLI полностью совместим с Qwen Code.

## SPEC-F-004 — Требуется решение об изменении топологии Qwen runtime

Severity: major
Status: open
Problem: To resolve SPEC-F-001 with the current official Qwen, the author proposes replacing the approved one-process/multiple-session topology with one contained Qwen process and one ACP session per logical thread, passing only that thread's artifact root through process-level `--include-directories`. This is feasible without ACP `additionalDirectories` and avoids exposing sibling artifact roots to one process, but it materially supersedes the topology approved in Decision D-018 and specified by REQ-006 and DEC-007, changing process ownership, resource usage, containment, cancellation, and close behavior. The reviewer cannot select this architectural change without a user decision.
Location: REQ-006, REQ-007, REQ-015, REQ-016, DEC-007, AC-002, AC-004
Traces: REQ-006, REQ-007, REQ-015, REQ-016, DEC-007, AC-002, AC-004
Recommendation: Decide whether to approve the author's proposed topology of one contained Qwen process and one ACP session per logical thread, with the thread's external artifact root passed through `--include-directories`, and require the reworked specification to update all affected lifecycle, cancellation, compatibility, and acceptance criteria consistently. If the one-process/multiple-session topology must remain, the author needs a different feasible mechanism for the external artifact root.
Decision: fix
Decided-by: user
Rationale: User accepted the pending recommendation with /apply.

## SPEC-F-005 — Требуется утвердить объединённое разрешение SPEC-F-001 и SPEC-F-004

Severity: major
Status: open
Problem: Автор предлагает одновременно разрешить SPEC-F-001 и SPEC-F-004 заменой обязательной session-scoped ACP capability `additionalDirectories` на process-level `--include-directories` и переходом от одного общего Qwen process к отдельному contained process с одной ACP-сессией для каждого logical thread. Это делает внешний artifact root доступным текущему официальному Qwen и изолирует sibling artifact roots, но материально отменяет ранее утверждённую топологию Decision D-018 и меняет границы `Runtime`, стоимость процессов, владение lifecycle, а также семантику отмены и закрытия. Такое объединённое архитектурное решение может утвердить только пользователь.
Location: REQ-006, REQ-007, REQ-015, REQ-016, DEC-007, AC-002, AC-004
Traces: REQ-006, REQ-007, REQ-015, REQ-016, DEC-007, AC-002, AC-004
Recommendation: Явно решить, принимается ли предложение автора: один contained Qwen process и одна ACP-сессия на каждый logical thread, только его внешний artifact root через `--include-directories`, без требования ACP `additionalDirectories`. При принятии автор должен согласованно обновить topology, runtime ownership, запуск, cancellation, close, compatibility и acceptance criteria и зафиксировать supersession Decision D-018 в журнале решений.
Decision: pending
Decided-by: none
Rationale:
