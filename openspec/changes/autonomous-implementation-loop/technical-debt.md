# Technical debt — autonomous-implementation-loop

## TD-7.5-001 — replacement Explorer session may survive request cleanup

- Origin: task 7.5 initial review; affected locations: `internal/flows/impl_loop/explorer_routing.go` and `controlled_agent_call.go`.
- Status: `open`.
- Potential problem: request cleanup closes the originally captured Explorer session, while an internally recreated replacement can remain owned until the whole owner closes.
- Evidence: controlled-call technical retry replaces the session internally and does not return that effective session to the request cleanup.
- Expected impact: a provider runtime/thread may leak after an investigation that succeeds only after a technical failure.
- Classification: technical debt because it requires a technical-failure path, not ordinary successful routing or same-session shortening.
- Possible follow-up: return/close the effective request-scoped session or move its lifetime fully into the controlled-call abstraction.

## TD-7.5-002 — Explorer episode key is caller supplied

- Origin: task 7.5 initial review; affected location: `internal/flows/impl_loop/explorer_routing.go`.
- Status: `open`.
- Potential problem: any nonblank operation episode is accepted, so a future caller could accidentally choose a fresh key per request and reset the ten-investigation allowance.
- Evidence: routing does not bind the episode to controller-owned source phase/session state; no production caller exists yet.
- Expected impact: future integration could bypass the per-source-episode limit.
- Classification: technical debt because the bypass is not reachable through a current production route.
- Possible follow-up: derive or validate the episode from controller-owned source-phase/session state.

## TD-7.5-003 — Explorer boundary and recovery coverage is incomplete

- Origin: task 7.5 initial review; affected location: `internal/flows/impl_loop/explorer_routing_test.go`.
- Status: `open`.
- Potential problem: focused routing coverage does not exercise request 10 versus 11, reopened persisted state, or the complete integration boundary.
- Evidence: the initial test used one investigation with two technical attempts; durable counter primitives have earlier separate coverage.
- Expected impact: regressions in the principal episode boundary or recovery composition could escape the routing suite.
- Classification: technical debt as a test-quality gap; current primitives are independently covered.
- Possible follow-up: add minimal boundary/reopen integration cases when production routing is composed.

## TD-7.4-001 — файловая policy не связана с фактической ролью сессии

- Origin: task 7.4 initial review; affected locations: `internal/flows/impl_loop/controlled_agent_call.go` and `agent_call_guard.go`.
- Status: `open`.
- Potential problem: validation ties policy only to the call ID, not to `Session.Role`/`Expectation.Role`, and the guard role vocabulary does not represent all read-only roles. Future routing could therefore supply an executor policy to a read-only role.
- Evidence: there are no production call sites yet; current validation does not enforce role consistency or derive write scope from the session role.
- Expected impact: a future integration may weaken post-call enforcement or attribute a violation to the wrong role.
- Classification: technical debt because the problematic route is not currently reachable in production and no typical supported-use failure exists yet.
- Possible follow-up: derive policy from the session role, validate it against the response expectation, represent all roles in violation records, and give read-only roles no writable scope.

## TD-7.4-002 — joined terminal error может скрыть независимую cleanup-ошибку

- Origin: task 7.4 rereview cycle 2; affected location: `internal/flows/impl_loop/session_owner.go`, retry discard and session close.
- Status: `open`.
- Potential problem: a joined error containing an expected terminal sentinel is accepted unless it also contains the two known cleanup/containment sentinels, so an unrelated unwrapped process/client close failure can be hidden.
- Evidence: supported adapters may return raw `process.Close` or `client.Disconnect` errors, and `errors.Join(ErrTurnInterrupted, errors.New("process close failed"))` satisfies the current expected-discard predicate.
- Expected impact: on a rare unclassified cleanup failure, work may continue in the replacement runtime without confirmed disposal of the old runtime.
- Classification: technical debt because cleanup failures are atypical and the ordinary timeout/retry path now behaves correctly.
- Possible follow-up: evaluate handle and runtime close results separately, ignore only the terminal handle result, preserve every independent runtime-close failure, and remove a replacement from the owner when discard fails.

## TASK-4.3-007 — closing content race in a pre-dirty tracked file

- Origin: task 4.3 final task rereview; affected location: `internal/gitsnapshot/snapshot.go`, `captureLocal` and `verifyLocalState`.
- Status: `open`.
- Potential problem: if a tracked file is already dirty with content A and changes to B after the second-pass temporary-index `git add -A`, the captured tree may still describe A while HEAD, symbolic ref, raw index and porcelain status remain unchanged. `EnsureUnchanged` can then accept the snapshot and attribute B to the next operation.
- Evidence: the reviewer identified a deterministic injected interleaving; no evidence established that this race is likely in typical supported operation.
- Expected impact: an externally concurrent edit could be attributed to the following controlled operation instead of causing an immediate pause.
- Classification: technical debt by explicit user disposition. The approved design states that the first version is not strict protection against external editors/Git and lists reliable separation of agent actions from simultaneous external editing as non-blocking future hardening. The specification therefore does not require the stronger atomic/continuous observation guarantee needed to close every such interleaving.
- Possible follow-up: repeat and compare the synthesized local content tree during closing verification while preserving linear submodule traversal, with a deterministic dirty-A-to-B regression.

## TASK-4.4-003 — guard regression does not isolate filter-equivalent detection

- Origin: task 4.4 rereview cycle 2; affected location: `internal/flows/impl_loop/agent_call_guard_test.go`.
- Status: `open`.
- Potential problem: the new guard regression changes normalized content from `before` to `agent`; the filtered synthetic tree therefore changes, so the test would still pass if the exact byte/mode path merge were removed.
- Evidence: production `Diff` correctly merges exact additions, deletions, byte changes, and mode changes, but the guard-level test does not isolate the same-filtered-blob case fixed by `TASK-4.4-002`.
- Expected impact: a future regression in filter-equivalent detection may not be caught at the guard integration layer.
- Classification: technical debt because review found the production behavior correct and identified only an acceptance-coverage weakness, not a current implementation failure.
- Possible follow-up: use CRLF-to-LF-only content and a custom filter mapping distinct raw inputs to the same blob; assert retry, journal evidence, and exact restoration.

## TASK-5.1-001 — failed-command output lacks a dedicated assertion

- Origin: task 5.1 initial review; affected location: `internal/checkexec/checkexec_test.go`.
- Status: `open`.
- Potential problem: the non-zero-exit fixture verifies exit code 23 but emits no stdout or stderr, so a future regression discarding or swapping failed-command output would not be caught.
- Evidence: production `Run` copies both buffers before returning the wrapped process error; successful execution covers stdout indirectly, but failed stdout/stderr are not asserted.
- Expected impact: weakened regression protection for diagnostics used by later log/reporting tasks.
- Classification: technical debt because current production behavior is correct by inspection and the gap is limited to test coverage.
- Possible follow-up: emit distinct stdout/stderr payloads before non-zero exit and assert both byte-for-byte with the exit code and error.

## TASK-5.2-002 — pre-canceled context can still launch a command

- Origin: task 5.2 initial review; affected location: `internal/checkexec/checkexec.go`.
- Status: `open`.
- Potential problem: `RunContext` checks the context only after starting and assigning the process, so an already-canceled or expired context can permit brief command side effects.
- Evidence: the start path precedes the select on `runContext.Done`; Windows assignment resumes the suspended process before cancellation is observed.
- Expected impact: a narrow pause/cancel-before-dispatch interleaving may run a command briefly.
- Classification: technical debt because review found no evidence that this narrow transition is typical or causes a serious supported-use failure.
- Possible follow-up: reject pre-canceled and pre-expired contexts before creating or starting the child, with deterministic tests.

## TASK-5.2-003 — supervisor failure has inconsistent result/error classification

- Origin: task 5.2 initial review; affected location: `internal/checkexec/checkexec.go`.
- Status: `open`.
- Potential problem: after cancellation, a `job.Close` error returns `ErrInfrastructure` while `Result.Failure` remains timeout or canceled.
- Evidence: supervisor-close failure overrides the returned error classification after the result was populated from the initiating context; no injectable regression covers this path.
- Expected impact: later orchestration could route a rare supervisor failure inconsistently depending on whether it reads the result or error chain.
- Classification: technical debt because supervisor-close failures are uncommon and no ordinary supported-use reproduction was established.
- Possible follow-up: make infrastructure the primary result failure while preserving cancellation as secondary diagnostic context, and inject supervisor/process launch for tests.

## TASK-5.4-002 — full-log paths need a later read-only agent access seam

- Origin: task 5.4 initial review; affected locations: `internal/flows/impl_loop/check_presentation.go`, `internal/runstore/store.go`, and later runtime adapters.
- Status: `open`.
- Potential problem: canonical log paths are inside the shared run files directory, while current adapters expose external paths through a writable ArtifactRoot or reject them. Granting that whole root would expose unrelated evidence; a normal per-session root leaves the paths unreadable, and Nessy readers reject non-UTF-8 logs.
- Evidence: current task proves controller-side verified reads, but role/context integration is scheduled for later tasks and has no read-only selected-evidence view yet.
- Expected impact: later integration could make full logs inaccessible to agents or grant overly broad artifact access.
- Classification: technical debt because adapter/context integration is explicitly later and strict run-state isolation is deferred; current persistence behavior is correct.
- Possible follow-up: add a controller-mediated verified reader or a dedicated read-only view containing only referenced logs, with cross-adapter conformance tests including invalid UTF-8.

## TASK-5.4-003 — reporter failure can leave a structurally successful set

- Origin: task 5.4 initial review; affected location: `internal/flows/impl_loop/check_set.go`.
- Status: `open`.
- Potential problem: when reporting fails after a successful command, the current result may remain `succeeded` and the tail is not marked `not_run`; `CheckSet.Succeeded()` can be true alongside the non-nil persistence error.
- Evidence: no unpublished reference escapes and correct callers treat the error as authoritative, but no reporter-failure regression enforces the structural state.
- Expected impact: later code that separates set inspection from error handling could treat unusable evidence as successful.
- Classification: technical debt because correct idiomatic callers remain safe and publication failures are uncommon.
- Possible follow-up: mark the current result failed/unusable and the tail not-run on reporter errors, and test failures after partial publication and on the final check.

## TASK-5.5-003 — mixed protected-and-allowed restoration lacks direct coverage

- Origin: task 5.5 initial review; affected location: `internal/flows/impl_loop/check_workspace_test.go`.
- Status: `open`.
- Potential problem: the protected-write regression changes only a protected path, so it does not directly cover one command changing both protected and allowed files.
- Evidence: production appears to preserve the allowed output, republish restored state, update assignment diff, and invalidate acceptance, but the combined path is not guarded by a test despite progress originally claiming it.
- Expected impact: preservation of allowed output and freshness after protected restoration could regress unnoticed.
- Classification: technical debt because review found no current production defect in this path.
- Possible follow-up: assert that a mixed write restores/journals the protected path, retains the allowed path in assignment diff, and reopens acceptance against restored final state.

## TASK-5.2-005 — Darwin processjob test can mishandle an empty PID file

- Origin: task 5.2 rereview cycle 2; affected location: `internal/processjob/job_darwin_test.go`.
- Status: `open`.
- Potential problem: when `ReadFile` succeeds with temporarily empty content, the polling helper falls through to `t.Fatal(nil)` instead of continuing.
- Evidence: the shell redirection creates/truncates the PID file before `printf` writes, leaving a short observable empty-file interval.
- Expected impact: low-probability native Darwin test flake; production behavior is unaffected and deferred cleanup still runs.
- Classification: technical debt because the race window is very short and no evidence establishes typical incidence.
- Possible follow-up: continue polling on empty trimmed content and fail only on a non-nil error other than `IsNotExist` or malformed non-empty content.

## TD-6.3-001 — active Close can be classified as an operator interrupt

- Origin: task 6.3 initial review; affected location: `internal/agentruntime/claudeapp/runtime.go` (`RunTurn` receive wait and `runtimeError`).
- Status: `open`.
- Potential problem: while a turn is active, a caller invoking `Close()` can receive either `ErrRuntimeClosed` or `ErrTurnInterrupted` depending on whether the response receiver or the canceled context wins the wait.
- Evidence: `runtimeError` treats `context.Canceled` as interruption even when `Interrupt()` was not called; the task's new interruption test covers explicit `Interrupt()` but not concurrent `Close()` while waiting for a terminal response.
- Expected impact: downstream orchestration may record an ordinary runtime shutdown as an operator interruption and select the wrong recovery reason.
- Classification: technical debt because the result depends on a narrow timing interleaving and neither an explicit deterministic-close guarantee nor evidence of a serious highly likely typical-use failure was established.
- Possible follow-up: classify cancellation with priority `interrupted` then `closed`, and add a deterministic active-turn `Close()` regression.

## TD-6.4-001 — Nessy production write-policy wiring lacks integration coverage

- Origin: task 6.4 initial review; affected locations: `internal/agentruntime/nessyapp/thread.go`, `preflight.go`, `conformance_test.go`, and `permissions_test.go`.
- Status: `open`.
- Potential problem: conformance exercises the file policy directly and the two-session regression manually configures it, so a future regression in the production `StartThread` → process → connection handoff could escape those tests.
- Evidence: changing the `WorkspaceWriteAllowed` value passed by `thread.go` would not fail the new direct policy tests, although a real write-enabled session could become read-only or vice versa.
- Expected impact: reduced regression protection for the authority handoff and session correlation, including the separately preserved authorization environment.
- Classification: technical debt because current production wiring is correct by inspection, local permission correlation and connection isolation are tested, and real-provider smoke is explicitly deferred to task 14.3.
- Possible follow-up: add a fake-ACP integration test creating writer and reader threads through `Runtime.StartThread`, driving real permission requests, and checking correlation, ArtifactRoot separation, and authorization environment preservation.

## TD-6.5-001 — implementation-loop role lists are duplicated

- Origin: task 6.5 initial review; affected locations: `internal/implementationconfig/merge.go` and `internal/flows/impl_loop/runtime_factory.go`.
- Status: `open`.
- Potential problem: the six loop roles are independently listed for configuration validation and runtime preparation.
- Evidence: both current lists contain the same roles, but there is no shared canonical source or compile-time/test relationship between them.
- Expected impact: a future role addition could be validated without runtime preflight or required at runtime without being accepted by configuration.
- Classification: technical debt because the lists currently agree and no present behavior is broken.
- Possible follow-up: expose an immutable copy of one canonical role list and use it in both stages.

## TD-6.5-002 — Nessy preflight defers envelope-schema validation

- Origin: task 6.5 rereview cycle 1; affected locations: `internal/agentruntime/nessyapp/config.go`, `runtime.go`, and `internal/implementationruntime/factories.go`.
- Status: `open`.
- Potential problem: Nessy `ValidateRuntimeConfig` omits `EnvelopeSchema`, although `StartRuntime` later validates it.
- Evidence: with an installed CLI and otherwise valid configuration, an empty or malformed controller-owned schema can pass role preparation and fail only when `PreparedRole.Start` creates the runtime.
- Expected impact: a configuration error may be discovered after the intended all-role preflight rather than before implementation starts.
- Classification: technical debt because the envelope schema is controller-owned and response-schema integration is scheduled in later tasks; no ordinary user configuration currently supplies this value.
- Possible follow-up: reuse full schema-object validation in `ValidateRuntimeConfig` and cover malformed and empty envelope schemas.

## TD-8.1-001 — OpenSpec document loading follows symlinks outside the repository

- Origin: task 8.1 independent review; affected locations: `internal/openspec/package.go` (`Load`, `readDocument`).
- Status: `open`.
- Potential problem: required documents are read through symlink-following filesystem calls while repository containment is checked only against the unresolved lexical path.
- Evidence: a symlinked change directory or required Markdown document can resolve outside the repository and its bytes can enter `CompleteSpecification`.
- Expected impact: in an atypical or hostile symlinked checkout, external local data or untrusted instructions could enter agent context.
- Classification: technical debt because the trigger requires an unusual or hostile checkout and no explicit approved requirement mandates canonical symlink containment.
- Possible follow-up: canonicalize repository and document paths, reject resolved paths outside the canonical root, and add a platform-appropriate symlink regression.

## TD-8.1-002 — exposed package values can diverge from their recorded versions

- Origin: task 8.1 independent review; affected locations: `internal/openspec/package.go` (`Document`, `Package`, `Documents`, `CompleteSpecification`).
- Status: `open`.
- Potential problem: exported mutable fields and slices allow a caller to change or reorder loaded content after `Load` without changing document or aggregate versions.
- Evidence: assigning a new `MainSpecs[0].Content` or reordering the slice changes rendered agent context while `Package.Version` remains unchanged.
- Expected impact: a future consumer could bind an agent response to a stale input version.
- Classification: technical debt because current callers do not mutate the returned package and typical supported behavior is not presently broken.
- Possible follow-up: keep loaded state private with defensive-copy accessors, or bind rendering and versioning to an immutable snapshot; add mutation-resistance coverage.

## TD-8.2-001 — Windows change-name case alias can bypass prior-run detection

- Origin: task 8.2 initial review; affected location: `internal/flows/impl_loop/initial_tasks.go` (`changeHasRun`).
- Status: `open`.
- Potential problem: saved change identity comparison is case-sensitive while a case-insensitive filesystem can resolve differently cased names to the same OpenSpec directory.
- Evidence: a closed run for `change` does not compare equal to requested `CHANGE`, although Windows may load the same package directory.
- Expected impact: an unusually cased invocation could reopen the same logical change after a closed run.
- Classification: technical debt because it depends on atypical noncanonical input and the approved contract does not define case normalization for change identifiers.
- Possible follow-up: define a canonical change identity or compare canonical package-directory identity, with a Windows regression.

## TD-8.2-002 — resume repeats comparison against the raw saved work-copy path

- Origin: task 8.2 rereview cycle 1; affected location: `internal/flows/impl_loop/controller_lock.go` (`FindUnclosedRun`).
- Status: `open`.
- Potential problem: a cleaned saved work-copy identity can pass the first comparison but a redundant second comparison uses the raw path and declines the same run.
- Evidence: a stored path with `.` or a trailing separator may be equivalent after cleaning but differ in the raw confirmation.
- Expected impact: an unusually noncanonical saved identity may not be resumable.
- Classification: technical debt because normal controller lease-derived identities are canonical.
- Possible follow-up: return after the single cleaned identity comparison and cover an equivalent noncanonical stored path.

## TD-8.2-003 — failed extraction persistence can leave the caller's model ahead of durable state

- Origin: task 8.2 rereview cycle 1; affected location: `internal/flows/impl_loop/initial_tasks.go` (`PersistInitialTaskExtraction`).
- Status: `open`.
- Potential problem: the caller's run is mutated before `StateStore.Record`, so a pre-event persistence failure can leave memory completed while the journal remains pending.
- Evidence: an already-cancelled record context can fail before durable publication after tasks/result were installed in memory.
- Expected impact: same-process retry may reject extraction until the model is reloaded; careless callers could inspect state inconsistent with the durable source of truth.
- Classification: technical debt because recovery from the journal restores the correct pending state and the failure path is uncommon.
- Possible follow-up: mutate a cloned candidate and publish/update the caller only after durable success, matching runstore transition helpers.

## TD-8.3-001 — baseline gate accepts an empty required-check selection

- Origin: task 8.3 initial review; affected location: `internal/flows/impl_loop/initial_checks.go` (`validateInitialRequiredChecks`).
- Status: `open`.
- Potential problem: an empty required list yields a zero-result set whose `Succeeded` method is vacuously true.
- Evidence: the gate itself does not reject empty `Selection.Required`.
- Expected impact: malformed direct API use could claim baseline success without checks.
- Classification: technical debt because normal configuration preparation already requires a non-empty required set.
- Possible follow-up: reject an empty required selection at the gate and add a small validation test.

## TD-8.3-002 — baseline transitions can leave in-memory state ahead of persistence

- Origin: task 8.3 initial review; affected location: `internal/flows/impl_loop/initial_checks.go` (operation/result/pause transitions).
- Status: `open`.
- Potential problem: the caller model is mutated before several `StateStore.Record` calls, so a pre-journal persistence failure can leave memory ahead of the durable source of truth.
- Evidence: operation addition, result/current-state update, and pause occur on the supplied run before their respective record calls.
- Expected impact: same-process retry may reject work until the run is reloaded from the journal.
- Classification: technical debt because recovery from durable state remains correct and the trigger is an uncommon persistence failure.
- Possible follow-up: apply transitions to clones and replace the caller only after durable publication, following runstore transition helpers.

## TD-8.3-003 — cancellation during initial snapshot can miss durable pause

- Origin: task 8.3 rereview cycle 1; affected location: `internal/flows/impl_loop/initial_checks.go` (workspace observer construction).
- Status: `open`.
- Potential problem: cancellation during initial workspace-snapshot construction reaches the pause path before the detached persistence context is created.
- Evidence: observer construction uses the invocation context, while the bounded `WithoutCancel` persistence context is initialized only afterward.
- Expected impact: a narrow pre-command cancellation can leave the durable run active with only a started attempt.
- Classification: technical debt because it requires cancellation in a short snapshot-construction window; active-command cancellation is durably handled.
- Possible follow-up: create the bounded detached persistence context immediately after attempt reservation and use it for every later pause path.

## STD-8.3-001 — baseline evidence validation predicates are duplicated

- Origin: task 8.3 rereview cycle 1; affected location: `internal/implementationstate/state.go` (`RecordInitialBaselinePass`, `validateInitialBaseline`).
- Status: `open`.
- Potential problem: two copies of the baseline operation/result predicate can drift as evidence rules evolve.
- Evidence: record-time validation and restored-state validation separately enumerate the same operation kind, counter, status, state, and basis relationships.
- Expected impact: future maintenance could accept evidence in one path and reject it in the other.
- Classification: technical debt because current predicates agree and no present behavior is broken.
- Possible follow-up: construct `InitialBaselineEvidence` once and validate it through the shared helper.

## TD-8.4-001 — briefer read-only policy can still permit explicit writable paths

- Origin: task 8.4 initial review; affected location: `internal/flows/impl_loop/agent_call_guard.go` (`normalizeAgentCallPolicy`).
- Status: `open`.
- Potential problem: the briefer policy rejects `AllowUnprotected` but does not require `AllowedPaths` and `AllowedRoots` to be empty, so a misconfigured call can authorize writes for this read-only role.
- Evidence: `AgentRoleBriefer` follows the shared allowlist path after the boolean read-only check.
- Expected impact: an incorrectly constructed future briefer call could accept writes to explicitly allowed locations.
- Classification: technical debt because the ordinary briefer call uses empty writable allowlists and no typical supported flow currently grants them.
- Possible follow-up: require empty writable allowlists for the briefer and add a focused policy regression.

## TD-8.4-002 — assignment persistence failure can leave the caller model ahead of durable state

- Origin: task 8.4 initial review; affected location: `internal/flows/impl_loop/brief_selection.go` (`ExecuteBriefSelection`).
- Status: `open`.
- Potential problem: `StartAssignment` mutates the supplied run before `StateStore.Record` uses the invocation context; cancellation or persistence failure can leave memory with an assignment absent from the journal.
- Evidence: the transition and persistence happen sequentially on the caller-owned model without a durable clone/update boundary.
- Expected impact: a same-process retry may observe inconsistent assignment state until reloading from the journal.
- Classification: technical debt because this requires a narrow cancellation or persistence-failure timing and journal recovery remains authoritative.
- Possible follow-up: transition a clone and publish with a bounded controller persistence context before replacing caller state.

## TD-8.4-003 — committed-task reselection lacks a direct regression

- Origin: task 8.4 initial review; affected location: `internal/flows/impl_loop/brief_selection_test.go`.
- Status: `open`.
- Potential problem: tests reject selection while an assignment is still active but do not directly exercise a response that reselects an already committed task when the next pending prefix has advanced.
- Evidence: the existing reselection fixture stops at an active assignment; production uses `PendingLeafTasks` and appears correct by inspection.
- Expected impact: a future regression in pending-task filtering might not be caught by task-specific coverage.
- Classification: technical debt because current production prefix validation excludes committed tasks and no present behavior failure is demonstrated.
- Possible follow-up: commit task A in the fixture, reject a repeated A response, then accept B on retry.

## TD-8.5-001 — brief persistence failure can leave memory and an artifact ahead of the journal

- Origin: task 8.5 initial review; affected location: `internal/flows/impl_loop/brief_version.go` (`PersistBriefVersion`).
- Status: `open`.
- Potential problem: the Markdown is published and the caller's run is mutated before `StateStore.Record`; cancellation or persistence failure leaves an orphan artifact and in-memory brief version absent from durable state.
- Evidence: publication, `AddBriefVersion`, and state recording occur sequentially without a cloned durable transition or orphan-reuse rule.
- Expected impact: a same-process retry can allocate an unintended next version, while a restart can conflict when republishing the deterministic artifact ID with different content.
- Classification: technical debt because it requires an uncommon persistence/cancellation failure and normal execution remains correct.
- Possible follow-up: record a cloned candidate before updating caller memory and define recovery-safe artifact identity or verified orphan reuse.

## TD-8.5-002 — YAML identifier scalars can be interpreted as non-string values

- Origin: task 8.5 initial review; affected location: `internal/flows/impl_loop/brief_version.go` (`yamlScalar`).
- Status: `open`.
- Potential problem: unquoted alphanumeric identifiers such as `null`, `true`, or `123` are valid input IDs but ordinary YAML parsers interpret them as null, boolean, or number.
- Evidence: the renderer leaves every alphanumeric identifier unquoted and its validator compares the same textual rendering rather than YAML types.
- Expected impact: unusual identifiers can lose string identity for external consumers of the brief document.
- Classification: technical debt because conventional assignment/task IDs avoid YAML-reserved scalar forms.
- Possible follow-up: always encode identifier fields as YAML strings and add reserved-word/numeric fixtures.

## TD-9.1-001 — transition result exposes raw check data beyond the bounded agent presentation

- Origin: task 9.1 initial review; affected locations: `internal/flows/impl_loop/implementer_transition.go` (`ImplementerTransitionResult`), `internal/flows/impl_loop/check_set.go`, and `internal/checkexec` result types.
- Status: `open`.
- Potential problem: `ImplementerTransitionResult.Set` exposes command environment and complete stdout/stderr through raw check results even though the transition describes its output as bounded agent feedback.
- Evidence: the exported result includes the full `CheckSet`; safe JSON evidence omits those fields, but a future formatter could consume the raw values directly.
- Expected impact: a future continuation boundary could accidentally disclose environment values or unbounded output instead of using `CheckPresentation` and durable references.
- Classification: technical debt because no current production continuation formatter consumes the raw fields and the persisted agent-facing evidence is already filtered.
- Possible follow-up: expose a dedicated agent-facing DTO and keep raw command/results private to controller execution.

## TD-9.3-001 — review-policy validation relies on lexical heuristics

- Origin: task 9.3 initial review; affected location: `internal/flows/impl_loop/task_review.go` (`blockingFindingBasis`, `reviewPassExplainsTestChange`).
- Status: `open`.
- Potential problem: multilingual substring checks can accept an unsupported assertion containing a magic word or reject a valid explanation phrased differently.
- Evidence: semantic grounds and test-change rationale are inferred from loose keyword presence rather than structured fields or controller evidence.
- Expected impact: unusual but valid reviewer wording can be rejected, while carefully worded unsupported claims can pass transport validation.
- Classification: technical debt because normal structured reviewer prompts constrain the wording and no current concrete behavior failure beyond the separately critical durable-discussion defects is demonstrated.
- Possible follow-up: replace prose heuristics with closed structured basis/rationale fields tied to controller evidence.

## TD-9.3-002 — task-review coverage is narrower than the required state space

- Origin: task 9.3 initial review; affected location: `internal/flows/impl_loop/task_review_test.go`.
- Status: `open`.
- Potential problem: initial tests omit direct coverage of the third-round boundary, retained-dispute branch, mixed finding resolution, untracked/generated files, changed-test rationale branches, durable restart reconstruction, and executor-session identity.
- Evidence: the frozen candidate's two principal tests exercise only a subset of those paths.
- Expected impact: regressions in uncommon review transitions may escape task-focused tests.
- Classification: technical debt for coverage not required to repair a demonstrated production defect; correction cycle 1 will add regressions required by the two critical findings.
- Possible follow-up: build a table-driven review state-machine suite spanning all response kinds and recovery boundaries.

## TD-9.3-003 — correction routing does not prove exact executor-session identity

- Origin: task 9.3 initial review; affected location: `internal/flows/impl_loop/task_review.go` (`RouteTaskReviewChanges`).
- Status: `open`.
- Potential problem: correction routing validates executor role and binding but not that the supplied live session is the exact session that produced the implementation.
- Evidence: another session with matching role/binding can satisfy the current checks.
- Expected impact: controller misuse could send review feedback to a replacement or unrelated executor session and break conversational continuity.
- Classification: technical debt because current controller ownership supplies the intended session and no cross-session production call site is yet demonstrated.
- Possible follow-up: persist and compare an immutable executor session identity at assignment creation and correction routing.

## TD-9.3-004 — untracked review diff starts one Git process per file

- Origin: task 9.3 rereview cycle 1; affected location: `internal/flows/impl_loop/task_review.go` (`assignmentDiff`).
- Status: `open`.
- Potential problem: each non-ignored untracked file is rendered by a separate `git diff --no-index` process.
- Evidence: untracked paths are enumerated safely, then processed sequentially through individual Git invocations.
- Expected impact: a workspace with a very large generated/untracked set can make preparation of the reviewer packet expensive.
- Classification: technical debt because completeness and safety are correct and ordinary assignment diffs contain a modest number of files.
- Possible follow-up: batch path-safe untracked diff generation or render equivalent patches in-process with bounded resources.

## TD-9.3-005 — persisted pending disputes are not deduplicated

- Origin: task 9.3 rereview cycle 1; affected locations: `internal/implementationstate/state.go`, `internal/flows/impl_loop/task_review.go`.
- Status: `open`.
- Potential problem: retry after dispute persistence but before reviewer completion can append the same semantic dispute again.
- Evidence: pending disputes are durable, but no stable dispute identity or idempotent append rule rejects a duplicate retry.
- Expected impact: reconstructed prior discussion may contain duplicate arguments and references after a narrow crash boundary.
- Classification: technical debt because reviewer ownership and recovery remain intact; duplication requires a retry in a small persistence/dispatch window.
- Possible follow-up: assign a controller-owned dispute ID and make persistence idempotent by assignment, round, and finding set.

## TD-9.3-006 — changes_requested may contain only resolved findings

- Origin: task 9.3 rereview cycle 1; affected location: `internal/flows/impl_loop/task_review.go`.
- Status: `open`.
- Potential problem: a `changes_requested` response in which every explicit decision is `resolved` is accepted, recording failed review evidence but yielding an empty correction packet.
- Evidence: per-finding validation accepts resolved decisions without requiring at least one open or retained finding for this response kind.
- Expected impact: an inconsistent reviewer response can route a pointless executor continuation instead of requiring `review_passed`.
- Classification: technical debt because normal reviewer instructions distinguish the response kinds and no loss of finding ownership occurs.
- Possible follow-up: require at least one open or retained decision for `changes_requested`, otherwise reject and retry as `review_passed`.

## TD-9.4-001 — refinement invalidates acceptance before a durable transition boundary

- Origin: task 9.4 initial review; affected locations: `internal/flows/impl_loop/brief_refinement.go`, `internal/implementationstate/state.go` (`BeginBriefRefinement`).
- Status: `open`.
- Potential problem: acceptance can be durably invalidated before current-diff capture succeeds.
- Evidence: correction cycle 1 moved transition work to a clone and removed the caller-memory divergence on a zero-event operation-record failure, but the reopened candidate is persisted before later Git/diff preparation.
- Expected impact: a Git/diff failure can leave the durable assignment reopened even though no new brief version was issued.
- Classification: technical debt because the transition is now internally consistent and retryable; the remaining effect requires a repository-read failure after durable operation reservation.
- Possible follow-up: capture all read-only inputs before publishing the cloned reopen/operation transition, or durably model a prepared refinement phase.

## TD-9.4-002 — impossible original-order coverage exercises substitution rather than irresolvability

- Origin: task 9.4 initial review; affected location: `internal/flows/impl_loop/brief_refinement_test.go` (`TestRefineBriefRejectsAnImpossibleOriginalTaskOrder`).
- Status: `open`.
- Potential problem: the test returns task `B` instead of `A`; it proves stable task-block enforcement but not a briefer conclusion that the original order itself cannot be implemented.
- Evidence: the fixture retries an invalid substituted task response and then accepts the original task, while general terminal clarification is tested separately.
- Expected impact: future regressions in the explicit impossible-order explanation/closure path may not be caught by a task-named regression.
- Classification: technical debt because production has a general material-specification closure route and no separate behavior defect is demonstrated.
- Possible follow-up: add a fixture whose briefer ties `clarification_required` to the fixed source-order conflict and verify complete durable closure evidence.

## TD-9.4-003 — Explorer continuation result may expose a closed session pointer

- Origin: task 9.4 rereview cycle 1; affected location: `internal/flows/impl_loop/brief_refinement.go` (`routeBriefRefinementExplorer`).
- Status: `open`.
- Potential problem: the returned `BriefRefinementResult.Call.Session` is taken from the pre-continuation turn even if continuation technically recreates the briefer session.
- Evidence: `SessionOwner` retains the replacement, but the result wrapper is assembled from the earlier session pointer.
- Expected impact: a caller relying directly on the returned session pointer after a rare technical recreation can receive a closed session, despite the controlled-call result contract promising the live session.
- Classification: technical debt because normal later routing looks up the replacement through `SessionOwner` and the issue requires a continuation retry/recreation.
- Possible follow-up: propagate the effective continuation session through the Explorer route result and assert it in a recreation regression.
## TD-9.5-001 — persisted pause can contain execution and limit reasons simultaneously

- Origin: task 9.5 initial review; affected location: `internal/implementationstate/state.go` (`validateLifecycle`).
- Status: `open`.
- Potential problem: validation accepts a paused state containing both `ExecutionBlock` and `LimitPause`, without tying `PauseReason` to one exclusive pause kind.
- Evidence: lifecycle validation checks both payloads independently but does not reject their coexistence.
- Expected impact: malformed replayed state or a future erroneous caller could make `Resume` both reset a limit counter and discard an execution block.
- Classification: technical debt because current public transition methods do not construct this combination; the trigger needs malformed state or future controller misuse.
- Possible follow-up: enforce mutually exclusive pause payloads and consistency between pause kind and reason.

## TD-9.5-002 — cancellation window before durable Explorer execution pause

- Origin: task 9.5 initial review; affected location: `internal/flows/impl_loop/explorer_routing.go` (`persistExplorerExecutionBlocked`).
- Status: `open`.
- Potential problem: cancellation after a valid Explorer `execution_blocked` response but before `StateStore.Record` can leave the run active after restart.
- Evidence: the accepted pause is persisted with the cancellable parent context.
- Expected impact: the diagnostic must be obtained again after the narrow cancellation interleaving.
- Classification: technical debt because it requires cancellation in a small post-response persistence window; ordinary execution persists the pause.
- Possible follow-up: persist the accepted block under a bounded non-cancellable context.
## TD-9.5-003 — check evidence and execution pause use separate durable events

- Origin: task 9.5 rereview cycle 1; affected location: `internal/flows/impl_loop/implementer_transition.go`.
- Status: `open`.
- Potential problem: environmental check failure evidence is persisted before the execution pause in a second state event.
- Evidence: process/host termination after the first record and before the second leaves restart state active with an environmental failure but no execution block.
- Expected impact: recovery could dispatch more work despite the already recorded environment failure.
- Classification: technical debt because it requires a narrow crash interleaving between two durable records.
- Possible follow-up: persist result and pause in one cloned-state event or reclassify the latest failed result during recovery before dispatch.
## TD-10.1-001 — acceptance reflection mutates live state before durable records

- Origin: task 10.1 initial review; affected location: `internal/flows/impl_loop/acceptance_reflection.go`.
- Status: `open`.
- Potential problem: acceptance, operation, and result transitions mutate the caller run before `StateStore.Record`, and existing reflection phases are not idempotently resumable.
- Evidence: a pre-durability failure leaves memory ahead of JSONL; after a projection-only failure, retry is rejected because the operation already exists. The post-call result boundary behaves similarly.
- Expected impact: transient persistence failures can strand the live controller or expose in-memory/durable divergence.
- Classification: technical debt because ordinary successful execution is correct and full restart reconciliation is partly assigned to later recovery tasks.
- Possible follow-up: mutate cloned candidates, adopt only after journal durability, and resume existing reflection phases idempotently.
## TD-10.2-001 — post-commit completion mutates live state before durable record

- Origin: task 10.2 initial review; affected location: `internal/flows/impl_loop/commit.go`.
- Status: `open`.
- Potential problem: assignment/task completion mutates the caller run before the journal/projection record succeeds.
- Evidence: a persistence failure leaves memory committed and makes direct retry conflict with terminal status.
- Expected impact: transient persistence failure can require restart reconciliation before later records proceed.
- Classification: technical debt because the Git commit remains identifiable and later recovery tasks explicitly reconcile interrupted Git operations.
- Possible follow-up: persist completion on a cloned candidate idempotently, then adopt it.

## TD-10.2-002 — commit repository target is weakly bound

- Origin: task 10.2 initial review; affected location: `internal/flows/impl_loop/commit.go` input validation.
- Status: `open`.
- Potential problem: the destructive repository target is checked only for non-emptiness.
- Evidence: a wiring or stale-path error can stage and commit a different repository while updating this run.
- Expected impact: wrong-repository local commit and incorrect run completion under controller misuse.
- Classification: technical debt because ordinary controller wiring supplies the selected repository and no current mismatched production caller is shown.
- Possible follow-up: canonicalize and bind the target to persisted work-copy/repository identity.

## TD-10.2-003 — Git message cleanup can break exact observation

- Origin: task 10.2 initial review; affected location: `internal/flows/impl_loop/commit.go` Git commit invocation.
- Status: `open`.
- Potential problem: `git commit -m` may apply configured cleanup while observation requires exact message equality.
- Evidence: cleanup-sensitive whitespace or blank lines can produce a commit and then fail intent comparison.
- Expected impact: a valid local commit can remain unacknowledged until recovery.
- Classification: technical debt because ordinary concise messages are unaffected and the trigger depends on message/configuration details.
- Possible follow-up: use `--cleanup=verbatim` and add a multiline cleanup-sensitive fixture.
## STD-10.3-001 — post-hook comparison omits index and submodule dimensions

- Origin: task 10.3 initial review; affected location: `internal/flows/impl_loop/commit.go` post-hook reconciliation.
- Status: `open`.
- Potential problem: reconciliation compares only HEAD and tree although snapshots also model ref, real index, status, and submodules.
- Evidence: index-only or submodule-only hook changes are not part of the committed-state invariant.
- Expected impact: uncommon hook behavior could complete against stale acceptance.
- Classification: technical debt because ordinary content-changing/refusing hooks are handled and these specialized hook mutations are not established as typical supported use.
- Possible follow-up: centralize the committed-state invariant in `gitsnapshot` and add tagged index/submodule hook fixtures.

## TD-10.3-001 — reconciliation transitions mutate live state before durability

- Origin: task 10.3 initial review; affected location: `internal/flows/impl_loop/commit.go` changed-commit and pause transitions.
- Status: `open`.
- Potential problem: reopen/pause mutates the caller run before persistence succeeds.
- Evidence: a storage failure leaves memory ahead of the journal and makes direct retry invalid.
- Expected impact: transient persistence errors can require restart reconciliation.
- Classification: technical debt because ordinary persistence succeeds and later recovery tasks cover interrupted Git reconciliation.
- Possible follow-up: transition a clone, record it durably, then adopt the candidate.
## TD-10.3-002 — reconciled commit evidence refs are not included in runstore reference validation

- Origin: task 10.3 rereview cycle 1; affected location: `internal/runstore/state.go` (`stateEvidenceRefs`).
- Status: `open`.
- Potential problem: `Assignment.ReconciledCommits` state/basis references are omitted from linked evidence verification.
- Evidence: orphaned or tampered reconciliation artifacts can escape the generic state reference walk.
- Expected impact: later recovery can trust incomplete reconciliation evidence until a route-specific read fails.
- Classification: technical debt because normal controller publication produces valid refs and no ordinary path corrupts them.
- Possible follow-up: include reconciled commit state and basis refs in the runstore evidence graph and add tamper/orphan tests.
## TD-10.4-001 — final transitions mutate live state before durability

- Origin: task 10.4 initial review; affected location: `internal/flows/impl_loop/final_review.go`.
- Status: `open`.
- Potential problem: final check, review, and success transitions mutate the caller run before state recording succeeds.
- Evidence: persistence failure can leave memory ahead of JSONL and make retry conflict with duplicate/terminal state.
- Expected impact: transient storage failures may require restart recovery.
- Classification: technical debt because ordinary durable execution succeeds and later recovery work covers interrupted operations.
- Possible follow-up: record cloned candidates and adopt only after durability.

## TD-10.4-002 — final-review route coverage is narrow

- Origin: task 10.4 initial review; affected location: `internal/flows/impl_loop/final_review_test.go`.
- Status: `open`.
- Potential problem: initial tests bypass parts of `StartFinalReview` and omit several response/recovery branches.
- Evidence: aggregate assembly, clarification, execution blocking, and persistence failures lack direct route coverage.
- Expected impact: uncommon transition regressions may escape focused tests.
- Classification: technical debt except for the explicit Explorer/freshness defects tracked as critical findings.
- Possible follow-up: add a table-driven final-review state-machine suite.

## TD-10.4-003 — final-review repository input is weakly bound

- Origin: task 10.4 initial review; affected location: `internal/flows/impl_loop/final_review.go` input validation.
- Status: `open`.
- Potential problem: repository is only nonempty, not canonicalized and bound to persisted work-copy identity.
- Evidence: controller miswiring could check/review another repository.
- Expected impact: incorrect final evidence under a future erroneous caller.
- Classification: technical debt because current controller wiring supplies the selected repository and no typical mismatched caller is demonstrated.
- Possible follow-up: bind canonical repository identity to durable run identity.

## TD-10.5-001 — informational tasks.md edits are not atomic across rejected turns

- Origin: task 10.5 initial review; affected location: `internal/flows/impl_loop/final_finding_tasks.go` agent-call and post-call validation path.
- Status: `open`.
- Potential problem: an allowed `tasks.md` edit survives a rejected structured response, so a retry may append again; non-append edits can also remain after the route returns an error.
- Evidence: response validation and append-only comparison occur after the orchestrator may edit the permitted path, without per-attempt restoration.
- Expected impact: duplicate or damaged informational Markdown entries can diverge from authoritative machine tasks after an erroneous agent turn.
- Classification: technical debt because machine state remains authoritative and the trigger requires an invalid/rejected orchestrator turn.
- Possible follow-up: make the append and response a per-attempt atomic transition or restore only `tasks.md` before retry/error; cover invalid-first/valid-second behavior.

## TD-10.5-002 — final-finding provenance is omitted from briefer context

- Origin: task 10.5 initial review; affected locations: `internal/implementationstate/state.go`, `internal/flows/impl_loop/role_context.go`.
- Status: `open`.
- Potential problem: corrective tasks persist final-review/finding identifiers, but briefer context exposes only ordinary task fields and not the finding problem, location, basis, or expected result.
- Evidence: `BuildBrieferStartContext` does not serialize `Task.FinalFindings` or the final-review receipt.
- Expected impact: correction quality depends on the orchestrator producing a sufficiently self-contained task title.
- Classification: technical debt because no explicit requirement mandates provenance in briefer context and a self-contained title may suffice.
- Possible follow-up: enrich briefer context with finding provenance/details or require a self-contained corrective-task description.

## TD-11.1-001 — user-control mutex does not serialize all shared Run mutations

- Origin: task 11.1 initial review; affected locations: `internal/flows/impl_loop/controlled_agent_call.go`, `internal/flows/impl_loop/user_control.go`, and state-replacement paths in `internal/runstore/state.go`.
- Status: `open`.
- Potential problem: agent registration and operation completion can read or replace the shared `Run` without participating in the `UserRunControl` mutex.
- Evidence: registration precedes durable attempt reservation, while state-store recording may replace the run concurrently with pause/close reading it under a different synchronization boundary.
- Expected impact: a narrow Go data race or stale transition candidate around operation start/completion.
- Classification: technical debt because the required interleaving is narrow and no high-likelihood failure in typical supported use is established.
- Possible follow-up: establish one serialized owner for all `Run` transitions or a shared synchronization boundary, with focused start/completion interleaving tests.

## TD-11.1-002 — two incompatible command-control integration paths remain exposed

- Origin: task 11.1 correction-cycle-1 rereview; affected locations: `internal/flows/impl_loop/user_control.go`, `initial_checks.go`, `implementer_transition.go`, and `final_review.go`.
- Status: `open`.
- Potential problem: the legacy `ControlledCheckRunner` controls only the low-level child, while corrected routes expose route-level `UserControl`; using the former alone recreates the narrow boundary and combining both produces nested registration/`ErrUserControlBusy`.
- Evidence: both public integration paths remain available, but only route-level control spans durable post-command bookkeeping.
- Expected impact: a future controller integration, notably task 12.1, could select or combine the wrong path.
- Classification: technical debt because no current production caller uses the obsolete path and the correct route-level API is present and tested.
- Possible follow-up: remove/deprecate `ControlledCheckRunner` or explicitly unwrap/reject it when route-level control is supplied, documenting one canonical integration path.

## TD-11.2-001 — projection-only failure can strand same-process reconciliation

- Origin: task 11.2 initial review; affected locations: `internal/flows/impl_loop/commit.go`, `internal/runstore/state.go`.
- Status: `open`.
- Potential problem: when the pending-intent JSONL append succeeds but SQLite projection fails, the live retry can reach Git before applying that pending projection; recording completion then conflicts with `ErrPendingEvent` until the store is reopened.
- Evidence: a nonzero durable event leaves the in-memory pending intent in place while the projection remains pending.
- Expected impact: a transient projection-only failure can strand same-process reconciliation, although restart recovery retains and identifies the commit safely.
- Classification: technical debt because it requires an injected or uncommon SQLite projection failure and ordinary execution is unaffected.
- Possible follow-up: resolve/project the durable pending intent before permitting Git retry, with a deterministic JSONL-success/SQLite-failure fixture.

## TD-11.2-002 — hook-changed retry can disconnect operation evidence from its trailer

- Origin: task 11.2 initial review; affected location: `internal/flows/impl_loop/commit.go` (`reconcileChangedCommit`).
- Status: `open`.
- Potential problem: a pending retry commits `intent.Message` but hook-change evidence can store a newer caller `OperationID`, so retained evidence and the actual `Stepan-Operation` trailer disagree.
- Evidence: the changed-commit route uses `input.OperationID` rather than the persisted intent operation after a pending retry.
- Expected impact: misleading recovery evidence on the uncommon combination of hook-modified content and a caller operation ID different from the durable intent.
- Classification: technical debt because no ordinary controller path demonstrating that combined trigger is established.
- Possible follow-up: consistently use `intent.OperationID` for reconciled evidence and artifact IDs once a pending intent exists.

## TD11.3-001 — failed SessionOwner replacement is not retried

- Origin: task 11.3 initial review; affected locations: `internal/flows/impl_loop/resume.go` session-owner replacement path.
- Status: `open`.
- Potential problem: the new configuration reference can become durable before old-session cleanup succeeds; the old owner is then closed but retained, and a later resume sees no configuration difference and does not retry replacement.
- Evidence: `SessionOwner.Close` marks the owner closed even when cleanup returns an error, while replacement is conditional on configuration equality.
- Expected impact: after an uncommon runtime cleanup failure, the next resume can activate with an unusable closed owner.
- Classification: technical debt because it requires a session cleanup failure during the narrow refresh transition.
- Possible follow-up: persist a pending-refresh marker or make session transition retry independently of configuration-reference equality.

## TD11.3-002 — session recreation coverage does not prove a usable runtime

- Origin: task 11.3 initial review; affected locations: `internal/flows/impl_loop/resume.go`, `internal/flows/impl_loop/resume_test.go`.
- Status: `open`.
- Potential problem: the recreation test permits nil factories and verifies only pointer replacement, yielding an owner whose first real role request would lack a prepared runtime.
- Evidence: factory-less preflight is skipped and the test never starts a role session with the changed profile/model.
- Expected impact: wiring regressions in applying reloaded profiles to usable sessions may escape focused coverage.
- Classification: technical debt because production can supply prepared factories and no ordinary broken wiring path is demonstrated.
- Possible follow-up: require prepared factories on the ready-to-run path and exercise an actual role session created from the reloaded profile.

## SMELL-001 — mandatory-check gate plumbing is duplicated

- Origin: task 11.4 initial review; affected locations: `internal/flows/impl_loop/resume_checks.go`, `initial_checks.go`, `final_review.go`.
- Status: `open`.
- Potential problem: publisher/observer setup, controlled execution, diagnostics, evidence persistence, and failure routing now have three similar implementations.
- Evidence: the resume gate repeats the pipeline while parameterizing only its own uncounted operation and pause semantics.
- Expected impact: future cancellation or durability fixes can drift across mandatory-check paths.
- Classification: technical debt because the current required behavior is correct outside the separate critical finding and no typical runtime failure follows from duplication alone.
- Possible follow-up: extract a shared mandatory-gate skeleton with explicit accounting, convergence, state-selection, and pause-policy parameters.

## TASK-11.5-D001 — production restart dispatcher coverage is incomplete

- Origin: task 11.5 initial review; affected locations: `internal/flows/impl_loop/resume.go`, `session_owner.go`, `resume_test.go`.
- Status: `open`.
- Potential problem: `Resume` creates an empty owner while `SessionOwner.Restore` is not invoked by production wiring and requires caller-built role contexts; focused tests fabricate contexts and cover only orchestrator and implementer.
- Evidence: durable enumeration and full-context reconstruction for briefer, task reviewer, final reviewer, and pending source/Explorer continuations are not exercised end-to-end.
- Expected impact: future production wiring can omit a required fresh role session or construct an incomplete context without focused recovery coverage detecting it.
- Classification: technical debt because the provider-neutral restoration primitives exist and no ordinary current production caller demonstrating a broken dispatch path is established.
- Possible follow-up: add a durable recovery dispatcher deriving active scopes/contexts from run and journal data, with all-role and pending-continuation tests.

## TASK-12.1-D001 — interactive driver lifecycle coverage is incomplete

- Origin: task 12.1 initial review; affected location: `internal/flows/impl_loop/interactive_test.go`.
- Status: `open`.
- Potential problem: focused tests directly dispatch lifecycle commands rather than driving successful `/implement` into blocked work and then entering `/pause` or `/stop` through the UI loop.
- Evidence: closed-state behavior is likewise exercised through policy/direct dispatch rather than an end-to-end fake prompt sequence.
- Expected impact: the explicit UI acceptance sequence can regress while direct controller tests remain green.
- Classification: technical debt because the production failure is tracked separately as critical and the missing sequences are a coverage gap.
- Possible follow-up: add fake-UI driver sequences for implement→blocked→pause, resume-check interruption, and stop→closed menu/state.

## TASK-12.1-D002 — lifecycle policy is duplicated across switches

- Origin: task 12.1 initial review; affected location: `internal/flows/impl_loop/interactive.go`.
- Status: `open`.
- Potential problem: command visibility and lifecycle-specific rejection messages are maintained in parallel switches.
- Evidence: policy and explanation branches independently enumerate lifecycle states.
- Expected impact: a future lifecycle change can make menu availability and unavailable explanations drift.
- Classification: technical debt because current branches are aligned and no behavior defect follows today.
- Possible follow-up: centralize visibility and unavailable reasons in one per-lifecycle policy table.

## TASK-12.1-D003 — successful user interruption is reported as an error

- Origin: task 12.1 correction-cycle-1 rereview; affected locations: `internal/flows/impl_loop/interactive.go`, `interactive_test.go`.
- Status: `open`.
- Potential problem: after a successful pause, the expected `ErrUserOperationInterrupted` from background work is surfaced as an error.
- Evidence: current driver reports the worker result independently of the successful lifecycle command.
- Expected impact: users see an alarming failure message after an intentional successful pause/stop.
- Classification: technical debt because durable lifecycle behavior is correct and this is error presentation rather than state loss.
- Possible follow-up: classify expected user interruption as normal lifecycle completion.

## TASK-12.1-D004 — active stop cancels context before controlled close

- Origin: task 12.1 correction-cycle-2 rereview; affected location: `internal/flows/impl_loop/interactive.go`.
- Status: `open`.
- Potential problem: `/stop` sends ordinary context cancellation to active controlled agent/check work before `UserRunControl.Close` can inject the intended interruption cause.
- Evidence: cancel-first is currently shared between resuming preflight and active controlled work.
- Expected impact: a route may record ordinary cancellation/failure bookkeeping before terminal closure, although final lifecycle state remains closed.
- Classification: technical debt because terminal stop is durable and the concern is uncommon diagnostic/history pollution rather than failure to stop.
- Possible follow-up: use cancel-first only for resume preflight; for active work close through `UserRunControl` first, then join.

## TASK-12.1-005 — unavailable pause can cancel an in-flight resume

- Origin: task 12.1 final rereview after correction cycle 3; affected location: `internal/flows/impl_loop/interactive.go` unavailable-command/pause drain path.
- Status: `open`.
- Potential problem: the driver treats a nil dispatch error as proof that `/pause` executed, although a known unavailable command also returns nil plus a concise explanation. A manually typed hidden `/pause` during `resuming` can therefore cancel and join the resume worker.
- Evidence: the unavailable paused-state branch returns a message without an error, after which the post-dispatch pause handling runs; with non-cancellable reconciliation this can also wait indefinitely.
- Expected impact: an unavailable command mutates execution by aborting `/resume` and may emit a secondary cancellation error, contradicting the contextual-command requirement.
- Classification: originally critical because it directly contradicts the explicit no-mutation behavior for unavailable commands. After the three-cycle global stop, the user explicitly directed on 2026-09-15 to record it as technical debt and continue; this entry preserves that waiver rather than claiming reviewer acceptance.
- Possible follow-up: use a typed dispatch outcome or confirm an actual active-to-paused transition before draining the worker; add a blocked-resume manual-`/pause` regression proving explanation-only behavior.

## TASK-12.2-D001 — startup work-copy identity honors redirected Git environment

- Origin: task 12.2 initial review; affected locations: `cmd/stepan/main.go`, `internal/flows/spec/workspace.go`.
- Status: `open`.
- Potential problem: startup reuses the planning resolver, which inherits `GIT_DIR`/`GIT_WORK_TREE`, rather than the implementation resolver that sanitizes Git-control variables.
- Evidence: implementation discovery receives the planning resolver's repository path.
- Expected impact: redirected Git environment can hide the current work-copy run or display another repository's run.
- Classification: technical debt because ordinary unredirected startup is unaffected and no typical production configuration establishing the trigger was shown.
- Possible follow-up: resolve implementation startup identity through `impl_loop.FindGitRoot` and add redirected-environment coverage.

## TASK-12.2-D002 — unrelated corrupt run can break no-run startup

- Origin: task 12.2 initial review; affected locations: `cmd/stepan/main.go`, `internal/flows/impl_loop/controller_lock.go`, `internal/runstore/state.go`.
- Status: `open`.
- Potential problem: ordinary startup validates every retained run before work-copy comparison, so malformed unrelated history aborts an application that has no implementation run for this repository.
- Evidence: only a wholly absent store is treated as no-run; errors from unrelated journals propagate.
- Expected impact: damaged retained history can prevent otherwise unchanged planning startup; discovery cost also grows with retained runs.
- Classification: technical debt because it requires unrelated stored corruption and does not break the normal healthy-store path.
- Possible follow-up: maintain trustworthy work-copy-addressable metadata/indexing and isolate unrelated history errors.

## TASK-12.2-D003 — startup materializes the full state journal

- Origin: task 12.2 correction-cycle-1 rereview; affected locations: `internal/runstore/state.go`, `internal/flows/impl_loop/startup.go`.
- Status: `open`.
- Potential problem: startup loads every full-state journal event although presentation needs only chronological transition information and a small window.
- Evidence: `JournalStates` builds an in-memory slice of all snapshots.
- Expected impact: retained recovery history can make startup memory proportional to the full journal and potentially quadratic in accumulated snapshot data.
- Classification: technical debt because ordinary current journals remain modest and no typical startup failure is demonstrated; performance/load validation was not requested.
- Possible follow-up: validate the stream while retaining only the necessary event/result window or explicit action metadata.

## TASK-12.2-D004 — resume specification evidence has duplicated private codecs

- Origin: task 12.2 exceptional correction-cycle-4 rereview; affected locations: `internal/flows/impl_loop/resume.go`, `cmd/stepan/main.go`.
- Status: `open`.
- Potential problem: the resume layer privately produces `Path`/`Version`/`Content` evidence while the application independently recreates its decoding and validation contract.
- Evidence: producer and classifier maintain separate anonymous JSON shapes and hash validation logic.
- Expected impact: future evidence-format changes can drift between producer and consumer and turn otherwise valid resumes into diagnostic pauses.
- Classification: technical debt because the current formats and validation agree and the remaining correction can preserve behavior without widening the typed interface.
- Possible follow-up: expose a typed specification-evidence codec at the implementation-loop boundary and reuse it in both producer and classifier.

## TASK-12.2-D005 — resumable-operation validation is duplicated across controllers

- Origin: task 12.2 exceptional correction-cycle-5 rereview; affected locations: `brief_selection.go`, `initial_checks.go`, `implementer_transition.go`, `task_review.go`, `final_review.go`, `acceptance_reflection.go`.
- Status: `open`.
- Potential problem: operation lookup, type/basis/description/result validation, and add-or-resume branching are repeated independently.
- Evidence: each controller contains its own compatible resume validation sequence.
- Expected impact: recovery-invariant changes require synchronized edits and can drift between stages.
- Classification: technical debt because current covered controllers enforce their local invariants and this refactor is not needed to close the remaining correctness finding.
- Possible follow-up: centralize typed operation-resumption validation in the durable-state layer.

## TASK-12.2-D006 — failed-review disputes cannot resume through the discussion route

- Origin: task 12.2 exceptional correction-cycle-5 rereview; affected locations: `restart_dispatch.go`, `implementer_transition.go`, `task_review.go`.
- Status: `open`.
- Potential problem: failed review recovery enters generic implementer validation, which rejects `review_disputed`, instead of forwarding the saved dispute through the existing reviewer discussion route.
- Evidence: the normal route supports `RouteTaskReviewChanges`/`RouteTaskReviewDispute`, while restart dispatch does not reconstruct that branch.
- Expected impact: a valid post-restart dispute can consume technical retries or pause instead of continuing discussion.
- Classification: technical debt because it affects the narrower dispute branch; ordinary accepted and changes-requested review recovery remains in the active correction scope.
- Possible follow-up: recover failed-review evidence and resume the existing reviewer discussion under its durable operation identity.

## TASK-12.2-008 — recovered execution-blocked response can replay forever

- Origin: task 12.2 exceptional correction-cycle-8 rereview; affected locations: `internal/flows/impl_loop/restart_dispatch.go`, `internal/implementationstate/state.go`.
- Status: `open`.
- Potential problem: replaying a durable `execution_blocked` response pauses the run without recording that the response was consumed, so remediation followed by another `/resume` selects and replays the same response again.
- Evidence: the recovered block creates neither a consumed-response result nor a downstream operation; latest-operation classification remains unchanged after the pause is cleared.
- Expected impact: some user-remediable blocks can enter a permanent resume/pause loop.
- Classification: originally critical because it violates restart liveness. The user explicitly directed after cycle 8 to record all subsequent findings as technical debt, accept task 12.2, and continue; this entry preserves that waiver rather than claiming reviewer acceptance.
- Possible follow-up: atomically persist a response-consumed/effect marker with the durable block and classify forward after remediation; add a remediation plus second-resume regression.

## TASK-12.2-009 — compatible refresh cannot replay a stale assignment receipt

- Origin: task 12.2 exceptional correction-cycle-8 rereview; affected locations: `internal/flows/impl_loop/restart_dispatch.go`, `internal/flows/impl_loop/controlled_agent_call.go`.
- Status: `open`.
- Potential problem: after a crash between durable agent success and response routing, a compatible specification/configuration refresh leaves the receipt and assignment operation bound to the old acceptance basis; restart selects it before checking basis and then rejects it against current inputs.
- Evidence: succeeded result-less assignment operations are classified before basis freshness, while receipt validation correctly refuses the stale binding.
- Expected impact: the combined crash-plus-compatible-refresh path durably pauses instead of continuing with fresh validation.
- Classification: originally critical because accepted compatible changes are required to resume. The user explicitly directed after cycle 8 to record all subsequent findings as technical debt, accept task 12.2, and continue; this entry preserves that waiver rather than claiming reviewer acceptance.
- Possible follow-up: retain stale receipts for audit, supersede the stale assignment operation with a current-basis operation, and add the combined refresh/replay regression.

## TASK-12.3-001 — progress is not refreshed during a multi-stage continuation

- Origin: task 12.3 initial review; affected location: `internal/flows/impl_loop/interactive.go`.
- Status: `open`.
- Potential problem: the progress formatter runs before the prompt and after the background continuation completes, not after intermediate agent/check transitions.
- Expected impact: a long autonomous continuation appears frozen and does not show stage-by-stage progress.
- Classification: originally critical because task 12.3 explicitly requires progress updates. Per the user's standing direction, all findings after the current 12.2 corrections are recorded as accepted technical debt and execution continues.
- Possible follow-up: publish immutable progress events or snapshots at durable controller transition boundaries.

## TASK-12.3-002 — displayed counters do not model semantic retry scopes

- Origin: task 12.3 initial review; affected location: `internal/flows/impl_loop/progress_presentation.go`.
- Status: `open`.
- Potential problem: UI totals technical attempts across history instead of presenting current resettable semantic counters.
- Expected impact: a technical retry can be displayed as a new review or implementation round.
- Classification: originally critical for misleading required counter presentation; explicitly accepted as debt by user direction.
- Possible follow-up: derive named counters from the durable semantic counter state and its reset scopes.

## TASK-12.3-003 — production UI cannot identify cross-build targets

- Origin: task 12.3 initial review; affected locations: `internal/flows/impl_loop/interactive.go`, `progress_presentation.go`.
- Status: `open`.
- Potential problem: the compile-only warning exists in a helper, but production does not provide recent check runs and persisted command presentation omits `GOOS`/`GOARCH` environment overrides.
- Expected impact: real Windows-to-Darwin cross-build progress cannot show the target or the required warning that compilation is not target runtime acceptance.
- Classification: originally critical for the explicit platform-reporting requirement; explicitly accepted as debt by user direction.
- Possible follow-up: persist structured target-platform metadata with check results and pass it into production progress snapshots.

## TASK-12.3-004 — progress formatting races the mutable run

- Origin: task 12.3 initial review; affected location: `internal/flows/impl_loop/interactive.go`.
- Status: `open`.
- Potential problem: foreground UI formatting reads the mutable `Run` while the background continuation may update the same object.
- Expected impact: ordinary progress rendering can race, show inconsistent data, or panic.
- Classification: originally critical for runtime correctness; explicitly accepted as debt by user direction.
- Possible follow-up: format immutable deep-copied snapshots published by the controller or synchronize all run access.

## TASK-12.3-D005 — progress presentation has boundedness and attribution gaps

- Origin: task 12.3 initial review; affected location: `internal/flows/impl_loop/progress_presentation.go`.
- Status: `open`.
- Potential problem: formatting rereads entire evidence files, latest assignment results can mask later run-level final results, role detection uses free-text matching, reader DTOs duplicate writer formats, initial orchestrator can appear as implementer, duration covers the whole worker, and ordinary pause/close reasons plus terminal summaries are generic.
- Expected impact: large logs increase UI cost and several uncommon stages can be attributed or summarized imprecisely.
- Classification: consolidated technical debt from the review's noncritical performance, maintainability, and presentation findings.
- Possible follow-up: use typed bounded progress events with structured role/platform/result metadata and operation-scoped timing/reasons.

## TASK-13.1-D001 — bootstrap profile input is not cancellation-aware

- Origin: task 13.1 initial review; affected location: `cmd/stepan/main.go`.
- Status: `open`.
- Potential problem: profile selection checks cancellation before `ReadString` but cannot interrupt a read already blocked waiting for newline.
- Expected impact: Ctrl+C during provider/model/reasoning input can wait until newline or EOF before shutdown.
- Classification: noncritical interaction/shutdown debt; completed input and task acceptance paths work.
- Possible follow-up: reuse a single context-aware stdin pump and select between input and `ctx.Done()`.
