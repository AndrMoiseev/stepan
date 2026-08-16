# Plan

All Python verification commands use \`python\`; when it is not on \`PATH\`, use \`py -3\` or an explicit Python 3 executable path with the same arguments.

## Accepted design risk

Finding \`DES-R-003\` remains an active, immutable accepted-risk input for the approved design. Its baseline is stage \`design\`, review hash \`sha256:47b3018b7484407bbdab2f2a4b4cb179178b1ad5fdf53ca8be565bf5288b7309\`, and exact user comment \`Давай считать дизайн утвержденным.\`. No step reopens, edits, or treats this finding as resolved. STEP-006, STEP-009, STEP-011, STEP-014, and STEP-015 verify downstream propagation and approval binding; any mismatch in the review, finding, or provenance must deactivate the record and follow the approved risk-invalidation flow.

## Steps

### STEP-001 — Expand the deterministic helper

Covers: REQ-030, REQ-031, REQ-034, REQ-036, REQ-037, REQ-102, DES-001, DES-004

Outcome: \`.agents/skills/sdd/scripts/sdd.py\` is the single Python-standard-library authority for exact source hashing, bounded \`spec-id\` derivation and suffix selection, and canonical SHA-256 of text, files, and JSON-compatible values.

Changes: Extend the helper's typed CLI/API operations and add deterministic source, collision, length-limit, file, text, and canonical-value golden fixtures.

Verification: Run the helper-focused unittest discovery with \`python -m unittest discover -p "test_sdd_helper*.py"\` (fallback \`py -3 -m unittest discover -p "test_sdd_helper*.py"\`); require byte-identical golden outputs, base/\`-2\`/\`-4\` collision selection of \`-3\`, clean-environment execution, and no imports outside the standard library.

### STEP-002 — Establish versioned protocol schemas

Covers: REQ-027, REQ-028, REQ-029, REQ-042, REQ-045, REQ-052, REQ-058, REQ-059, REQ-087, DES-003, DES-004, DES-005, DES-006, DES-007, DES-008

Outcome: Every provider-neutral machine record has a closed \`schema_version: 1\` JSON-subset-YAML contract that round-trips deterministically and rejects unknown, lossy, malformed, or cross-variant data before any write.

Changes: Add portable protocol types, deterministic UTF-8 sorted-key codecs, validators for state, manifests, provenance, reviews, risks, approvals, pending records, attempts, publication operations and commit intents, plus an explicit lossless-migration registry with no implicit downgrade.

Verification: Run schema fixtures with \`python -m unittest discover -p "test_sdd_schema*.py"\` (fallback \`py -3 ...\`); assert valid records round-trip with a trailing newline, all illegal state/pending/result shapes fail, and future-version or undeclared-migration fixtures leave the repository snapshot unchanged.

### STEP-003 — Validate artifacts, reviews, IDs, and traceability

Covers: REQ-003, REQ-018, REQ-019, REQ-020, REQ-021, REQ-022, REQ-023, REQ-024, REQ-025, REQ-026, REQ-050, REQ-053, REQ-054, REQ-055, REQ-056, DES-003

Outcome: Authored artifacts and latest review snapshots can be published only when their structure, stable identity, verdict semantics, complete coverage, and \`REQ-* -> DES-* -> STEP-*\` trace chain satisfy the approved contracts without hidden redesign.

Changes: Add Markdown structural/semantic validators, the append-only ID registry and tombstones, review snapshot validation, prior-review finding-ID checks, complete-package coverage checks, and valid/invalid fixtures.

Verification: Run \`python -m unittest discover -p "test_sdd_contract*.py"\` (fallback \`py -3 ...\`); observe rejection of missing/empty sections, duplicate or reassigned IDs, non-atomic requirements or steps, inconsistent verdicts, incomplete coverage, stale findings, and plan-only technical choices.

### STEP-004 — Build the canonical package and explicit start boundary

Covers: REQ-001, REQ-003, REQ-004, REQ-005, REQ-015, REQ-016, REQ-017, REQ-027, REQ-034, REQ-035, REQ-036, REQ-037, REQ-060, DES-001, DES-004

Outcome: Only \`start-new(source_payload)\` can create \`.stepan/specs/<spec-id>/\`, and it atomically publishes a resumable \`idea/drafting\` state with exact source provenance while leaving all later artifact and review paths absent.

Changes: Add the Router CLI/event surface, helper client, package repository, deterministic \`state.yaml\` initializer, collision-choice checkpoint, eight-path package mapping, lazy file creation, and provider-neutral canonical-record filtering.

Verification: Run package/start tests with \`python -m unittest discover -p "test_sdd_package*.py"\` (fallback \`py -3 ...\`); compare explicit and non-explicit requests, crash immediately after initialization, identical-source runs, occupied-ID choices, and final path inventories, with no mutation before a collision choice.

### STEP-005 — Execute the normative transition oracle

Covers: REQ-002, REQ-004, REQ-058, REQ-059, REQ-060, REQ-061, REQ-062, REQ-063, REQ-064, REQ-065, REQ-066, REQ-067, REQ-068, REQ-069, REQ-070, REQ-071, REQ-072, REQ-073, REQ-074, REQ-075, REQ-076, REQ-077, REQ-078, REQ-079, REQ-085, REQ-087, DES-007

Outcome: The Router evaluates Appendix A as the exhaustive first-match oracle, preserving its legal-state shapes, phase precedence, guard-failure/default immutability, counters, scheduled launches, retry policy, and terminal behavior exactly.

Changes: Add the machine-readable oracle loader/compiler, legal-state validator, derived-value and launch-profile resolver, guard/effect interfaces, phase pipeline, event dispatcher, and immutable invalid-event fallback.

Verification: Run oracle unit/model tests with \`python -m unittest discover -p "test_sdd_oracle*.py"\` (fallback \`py -3 ...\`); enumerate every schema-valid state/status/pending/event/guard combination and unspecified event, compare the selected row and effects, and assert at most one scheduled adapter call.

### STEP-006 — Implement provenance, approvals, and accepted risks

Covers: REQ-005, REQ-027, REQ-032, REQ-033, REQ-040, REQ-041, REQ-042, REQ-043, REQ-044, REQ-045, REQ-046, REQ-047, REQ-048, REQ-049, REQ-057, REQ-070, REQ-071, REQ-084, REQ-109, DES-005, DES-006

Outcome: Role-local manifests, output provenance, immutable approval baselines, and append-only accepted risks form a content-addressed dependency graph that propagates only active risks and invalidates every stale dependent edge.

Changes: Add the role-declaration resolver, normalized manifest builder/store, provenance graph, approval guard/baseline builder, accepted-risk map and activation rules, risk-input envelope projection, and dependency invalidation service.

Verification: Run provenance mutation scenarios with \`python -m unittest discover -p "test_sdd_provenance*.py"\` (fallback \`py -3 ...\`), independently changing content, path, presence, declaration, artifact, review, finding text, and risk inputs. Assert that \`DES-R-003\` is bound to the exact approved design review hash and user comment, reaches Planner and full-package review, participates in approval/commit guards, remains append-only, and deactivates rather than mutates when its baseline changes.

### STEP-007 — Isolate fresh role executions behind the runner contract

Covers: REQ-006, REQ-007, REQ-008, REQ-009, REQ-010, REQ-011, REQ-012, REQ-013, REQ-014, REQ-038, REQ-039, REQ-040, REQ-041, REQ-042, REQ-043, REQ-051, REQ-085, REQ-086, REQ-106, REQ-107, DES-002, DES-005

Outcome: \`run(invocation) -> result\` creates one fresh context with only the declared progressive-disclosure inputs, and only a current, correctly bound, closed result variant from its isolated workspace/outbox can reach Router publication.

Changes: Add protocol invocation/result unions, role/config loader, minimal-envelope builder, monotonic attempt identities, \`.router/attempts/<attempt-id>/workspace/\` and \`outbox/\` handling, fake runner, sandbox capability mapping, pre/post snapshots, and author/reviewer boundary validation.

Verification: Run runner tests with \`python -m unittest discover -p "test_sdd_runner*.py"\` (fallback \`py -3 ...\`); use parent/prior-dialog and other-role-input canaries, author sentinels, reviewer write attempts, malformed questions/unions, stale IDs, and invocation tampering to prove fresh contexts and zero unauthorized canonical mutation.

### STEP-008 — Publish and recover role results crash-safely

Covers: REQ-005, REQ-006, REQ-012, REQ-013, REQ-014, REQ-076, REQ-085, REQ-086, REQ-093, REQ-094, REQ-095, REQ-096, REQ-103, DES-002, DES-009

Outcome: A valid current role result is published exactly once through a recoverable per-spec transaction, while partial, forbidden, failed, abandoned, or late results cannot advance canonical state.

Changes: Add per-spec locking, \`.router/txn/<operation-id>/\` write-ahead metadata/candidates/before-images, same-directory atomic replacement and sync, publication reconciliation outcomes, active-attempt/outbox recovery, stale-result fencing, and Router-only quiescent cleanup.

Verification: Run publication tests with \`python -m unittest discover -p "test_sdd_publication*.py"\` (fallback \`py -3 ...\`), injecting a crash after every DES-009 boundary and role completion order. Resume each fixture twice and compare with the uninterrupted checkpoint; require reuse of complete valid outputs, rejection of incomplete/foreign outputs, and no duplicate role launch or publication.

### STEP-009 — Integrate reviews, revisions, risks, and approvals

Covers: REQ-003, REQ-004, REQ-021, REQ-032, REQ-045, REQ-047, REQ-048, REQ-049, REQ-050, REQ-051, REQ-052, REQ-053, REQ-054, REQ-055, REQ-056, REQ-057, REQ-061, REQ-062, REQ-063, REQ-064, REQ-065, REQ-066, REQ-067, REQ-068, REQ-069, REQ-070, REQ-071, REQ-072, REQ-073, DES-003, DES-006, DES-007

Outcome: The integrated normal lifecycle reaches each user checkpoint and finally \`plan/approved\` only through current reviews, bounded automatic revisions, explicit clarification/revision decisions, complete risk acceptance, and hash-bound approvals.

Changes: Wire author/reviewer result dispatch to oracle effects, review publication, revision inputs and counters, clarification persistence, checkpoint actions, accepted-risk transactions, downstream launch envelopes, and intermediate/final approval publication.

Verification: Run deterministic fake-runner lifecycle scenarios with \`python -m unittest discover -p "test_sdd_lifecycle*.py"\` (fallback \`py -3 ...\`) for pass, advisory-only pass, three automatic revisions, unresolved findings, accept-risk, user revision, clarification restart, ordinary role failure, stop/question/abort, and full idea-to-plan completion. Confirm the active \`DES-R-003\` record is in Planner/reviewer inputs and the final approval baseline.

### STEP-010 — Reconcile content and exceptional decisions

Covers: REQ-033, REQ-044, REQ-049, REQ-057, REQ-069, REQ-076, REQ-080, REQ-081, REQ-082, REQ-083, REQ-084, REQ-085, REQ-086, REQ-087, REQ-094, REQ-108, REQ-109, REQ-110, DES-005, DES-008

Outcome: One immutable-snapshot classifier deterministically selects and durably resolves manual mutation, stale input, review/risk invalidation, upstream revision, boundary violation, or no discrepancy without data loss or implicit retry.

Changes: Add the ranked whole-snapshot classifier, complete evidence/suspended-context records, attempt fencing, typed pending constructors, earliest-stage dependency invalidation, stale-file retention, and guarded handlers for \`revise\`, \`retry-role\`, review relaunch, and exposed risks.

Verification: Run reconciliation tests with \`python -m unittest discover -p "test_sdd_reconcile*.py"\` (fallback \`py -3 ...\`) across each class, every simultaneous-class ordering/tie, changed observations, failed guards, repeated resume, and every resolution. Assert exact old/new evidence, unchanged counters where required, preserved user/downstream bytes, and a single fresh launch only after successful resolution.

### STEP-011 — Add isolated Git approval checkpoints

Covers: REQ-076, REQ-088, REQ-089, REQ-090, REQ-091, REQ-092, REQ-093, REQ-094, REQ-097, REQ-098, REQ-099, REQ-100, REQ-101, REQ-111, REQ-112, REQ-113, DES-010

Outcome: Explicit approval commits contain only the canonical package with the exact message while preserving non-package worktree bytes and staged semantics across success, ordinary failure, conflict, retry, cancellation, revision, and crash recovery.

Changes: Add approval/isolation guards, complete snapshot and commit-intent records, approval and candidate-real alternate indexes, exact tree/commit construction, ref compare-and-swap, journaled real-index replacement, verification/recovery phases, and the three \`commit-conflict\` resolutions.

Verification: Run Git matrix tests with \`python -m unittest discover -p "test_sdd_git*.py"\` (fallback \`py -3 ...\`) over clean/dirty worktrees, staged non-package additions/modifications/deletions/modes/flags, package overlap, unsupported/unmerged indexes, symbolic/detached HEAD, external ref/index/package changes, and every CAS/index/state crash point. Require one reachable package-only commit on success, exact message, preserved non-package staged delta and worktree bytes, no package staged reversal, no automatic retry, and identical approval guards—including active \`DES-R-003\`—for \`continue\` and \`continue-and-commit\`.

### STEP-012 — Implement the repository-scoped Codex adapter

Covers: REQ-006, REQ-007, REQ-008, REQ-009, REQ-010, REQ-011, REQ-012, REQ-013, REQ-014, REQ-038, REQ-039, REQ-040, REQ-041, REQ-042, REQ-043, REQ-044, REQ-051, REQ-104, REQ-106, REQ-107, DES-002, DES-005, DES-011

Outcome: The first real adapter and six repository-scoped custom-agent overrides execute the same runner contract with reproducible role-local model, reasoning, sandbox, and progressive-disclosure input settings.

Changes: Add the Codex runner implementation and separate \`.codex/agents/*.toml\` overrides for Router, Framer, Specifier, Designer, Planner, and Spec Reviewer; keep concrete project paths only in the owning role's override.

Verification: Run Codex adapter conformance with \`python -m unittest discover -p "test_sdd_codex*.py"\` (fallback \`py -3 ...\`); inspect effective configurations and execute context canaries, per-role input canaries, author/reviewer boundaries, current/stale attempt binding, outbox tampering, and fresh-reviewer repetitions.

### STEP-013 — Complete contract, helper, and oracle fixtures

Covers: REQ-018, REQ-019, REQ-020, REQ-021, REQ-022, REQ-023, REQ-024, REQ-025, REQ-026, REQ-027, REQ-028, REQ-029, REQ-030, REQ-031, REQ-042, REQ-045, REQ-052, REQ-053, REQ-054, REQ-055, REQ-056, REQ-057, REQ-058, REQ-059, REQ-079, REQ-087, REQ-102, REQ-103, DES-003, DES-004, DES-007, DES-008, DES-011

Outcome: A table-driven fixture corpus proves every schema, artifact contract, stable-ID rule, helper golden, provenance edge, legal state, transition row, guard outcome, and immutable unspecified event.

Changes: Consolidate valid/invalid JSON-subset-YAML and Markdown fixtures, golden expected values, oracle row cases, state/event generators, mutation assertions, and coverage reports in the conformance harness.

Verification: Run \`python -m unittest discover -p "test_sdd_*fixture*.py"\` and \`python -m unittest discover -p "test_sdd_oracle*.py"\` (fallback \`py -3 ...\`); require zero uncovered oracle rows, events, state shapes, \`REQ-*\`, or \`DES-*\`, with byte equality only for deterministic helpers and already-published outputs.

### STEP-014 — Run crash and adapter golden conformance

Covers: REQ-003, REQ-005, REQ-006, REQ-007, REQ-008, REQ-009, REQ-010, REQ-011, REQ-012, REQ-013, REQ-014, REQ-033, REQ-041, REQ-042, REQ-043, REQ-044, REQ-045, REQ-046, REQ-047, REQ-048, REQ-049, REQ-050, REQ-051, REQ-057, REQ-076, REQ-080, REQ-081, REQ-082, REQ-083, REQ-084, REQ-085, REQ-086, REQ-093, REQ-094, REQ-095, REQ-096, REQ-097, REQ-098, REQ-099, REQ-100, REQ-101, REQ-102, REQ-103, REQ-104, REQ-105, REQ-106, REQ-107, REQ-108, REQ-109, REQ-110, REQ-111, REQ-112, REQ-113, DES-002, DES-005, DES-006, DES-008, DES-009, DES-010, DES-011

Outcome: One provider-neutral golden suite passes unchanged through the fake runner and Codex adapter across normal, exceptional, race, publication-crash, and Git-crash scenarios, while the portability claim remains gated on a future chosen second real adapter passing that identical suite.

Changes: Add end-to-end golden scenarios, process-level crash injection, live old-agent race fixtures, new-host resume harnesses, shared runner assertions, Codex bindings, and an explicit second-real-adapter conformance gate/report.

Verification: Run \`python -m unittest discover -p "test_sdd_conformance*.py"\` (fallback \`py -3 ...\`) for both fake and Codex runners, including every durable boundary named by DES-009 through DES-011 and both late-result completion orders. Require convergence to the uninterrupted hashes/checkpoint, no duplicate role result or commit, exact \`DES-R-003\` propagation/invalidation behavior, and a report that fake success alone is insufficient for REQ-105.

### STEP-015 — Document operation, migration, and release audit

Covers: REQ-001, REQ-002, REQ-004, REQ-005, REQ-015, REQ-016, REQ-027, REQ-028, REQ-029, REQ-030, REQ-031, REQ-038, REQ-039, REQ-040, REQ-041, REQ-073, REQ-075, REQ-076, REQ-077, REQ-078, REQ-079, REQ-088, REQ-089, REQ-090, REQ-091, REQ-092, REQ-093, REQ-094, REQ-095, REQ-096, REQ-097, REQ-098, REQ-099, REQ-100, REQ-101, REQ-104, REQ-105, DES-001, DES-002, DES-004, DES-007, DES-009, DES-010, DES-011

Outcome: Maintainers and users can start, inspect, resume, migrate, recover, configure, approve, and audit the SDD flow from repository files alone without overstating second-provider portability or the accepted design risk.

Changes: Add CLI/event documentation, package/schema v1 reference, explicit lossless-migration procedure and unknown-version stop behavior, role-override/project-input guide, recovery and Git-isolation runbooks, conformance instructions, portability-claim gate, and final package audit checklist.

Verification: Execute every documented command against disposable fixtures with \`python\` and the \`py -3\`/explicit-executable fallback, then start a new process with no conversation and recover the same checkpoint. Confirm the documentation reproduces \`plan/approved\`, explains that no implementation stage follows, preserves the exact immutable \`DES-R-003\` provenance and invalidation consequences, and does not claim a second real adapter before one is selected and passes the golden suite.

## Final verification

1. Run the complete standard-library suite with \`python -m unittest discover\` (fallback \`py -3 -m unittest discover\` or an explicit Python 3 executable), followed by \`git diff --check\`.
2. Run the generated transition-model report and require complete Appendix A row/state/event/guard coverage, immutable unspecified events, exact counters/effects/retry behavior, and no extra mutating transition.
3. Run helper goldens, schema/contract fixtures, provenance mutation scenarios, the publication/role crash matrix, and the full Git isolation/recovery matrix.
4. Run the identical end-to-end golden suite through fake and Codex runners; record second-real-adapter portability as unproven until a selected adapter passes the same suite.
5. From a fresh process with no chat history, audit repository files alone and require four non-empty artifacts, current required reviews, active approvals, \`stage: plan\`, \`status: approved\`, no provider-specific canonical identifiers, and complete \`REQ-001\` through \`REQ-113\` and \`DES-001\` through \`DES-011\` traceability.
6. Verify the active accepted-risk record for \`DES-R-003\` retains stage \`design\`, review hash \`sha256:47b3018b7484407bbdab2f2a4b4cb179178b1ad5fdf53ca8be565bf5288b7309\`, exact comment \`Давай считать дизайн утвержденным.\`, downstream Planner/reviewer/approval/commit provenance, and the approved invalidation behavior without reinterpretation.
