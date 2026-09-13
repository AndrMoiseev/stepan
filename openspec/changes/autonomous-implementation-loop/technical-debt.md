# Technical debt — autonomous-implementation-loop

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
