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
