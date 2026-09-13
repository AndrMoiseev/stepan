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
