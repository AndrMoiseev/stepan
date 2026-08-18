# Design

## Overview

The SDD framework is implemented as a repository-scoped, pre-development state machine with four ordered stages: `idea`, `requirements`, `design`, and `plan`. A deterministic Python Router owns all canonical mutations. It reads the durable package, validates an explicitly requested event, creates a minimal invocation for one fresh role agent when content work is required, validates that result, and publishes the result through a recoverable transaction. No decision depends on chat history or process memory.

The implementation has four boundaries:

1. **Portable protocol** — provider-neutral artifact schemas, role contracts, dependency rules, event/guard/effect table, and adapter conformance scenarios. It contains no repository-specific project-document paths.
2. **Router** — a Python 3 standard-library state-machine engine, validators, publication coordinator, recovery coordinator, and Git checkpoint coordinator. It is the sole writer of `state.yaml` and canonical review files.
3. **Deterministic helper** — `../../../embedded-framework/skills/sdd/scripts/sdd.py`, the sole implementation of `spec-id` derivation and canonical hashing. The Router invokes its CLI and treats its results as authoritative; it does not duplicate either algorithm.
4. **Adapter and role configurations** — a small runner interface plus repository-scoped `.codex/agents/*.toml` overrides for the first Codex adapter. Each override declares that role's prompt/configuration, project inputs, model, reasoning effort, and sandbox. Other providers can implement the same runner interface without changing the protocol or package.

Canonical flow data lives below `.stepan/specs/<spec-id>/`. Markdown is used for the four authored artifacts. Machine files use a deliberately small JSON data model serialized into the required `.yaml` paths; JSON is a YAML 1.2 subset and can be read and written deterministically with the Python standard library. Router-owned transaction files may temporarily exist under `.stepan/specs/<spec-id>/.router/`, are excluded from approval commits, and are removed after reconciliation.

## Decisions

### DES-001 — Repository package and explicit lifecycle

Covers: REQ-001, REQ-002, REQ-003, REQ-004, REQ-005, REQ-015, REQ-016, REQ-017, REQ-027, REQ-034, REQ-035, REQ-036, REQ-037

Decision: Expose a Router command/event API in which `start-new(source_payload)` is the only operation that can create a flow. The Router preserves the exact user text, excluding transport metadata, in the initial `state.yaml`, invokes the helper with that text, and stores the helper-returned source hash and `spec-id` before any role runs. If the base ID exists, the Router performs no package mutation until the user chooses resume or the helper-returned lowest free suffixed ID.

The package has these canonical paths and no alternate index:

- `idea.md`, `requirements.md`, `design.md`, and `plan.md`;
- `state.yaml`;
- `review/requirements.yaml`, `review/design.yaml`, and `review/plan.yaml`.

Files other than `state.yaml` are created lazily on their first valid publication. `state.yaml` is the package index and authority for source provenance, current stage/status, output hashes/provenance, manifests, accepted risks, approvals, pending decisions, ID tombstones, revision inputs, and recovery intent. Canonical records use protocol role names only; provider, model, thread, and session identifiers are runtime diagnostics and never enter the canonical package. The only successful terminal target in scope is `plan/approved`.

Rationale: A single durable package and a single creation event make resumption and collision handling deterministic. Persisting source provenance before Framer eliminates dependence on a parent conversation, while lazy files prevent incomplete placeholders from looking like completed work.

### DES-002 — Fresh isolated roles behind a narrow adapter

Covers: REQ-006, REQ-007, REQ-008, REQ-009, REQ-010, REQ-011, REQ-012, REQ-013, REQ-014, REQ-038, REQ-039, REQ-040, REQ-041, REQ-051, REQ-106, REQ-107

Decision: Define one adapter operation, `run(invocation) -> result`, with protocol-owned JSON-compatible types. Every call creates a new agent context without parent-chat or prior-agent turns. An invocation contains exactly: protocol role prompt, `spec_id`, durable `attempt_id`, resolved inputs declared by that role, explicitly named canonical artifacts, applicable active accepted-risk records, optional revision/clarification input, expected result type, and read/write capabilities. There is no catch-all repository context and no inherited conversation.

For Codex, keep separate repository-scoped TOML overrides for Router, Framer, Specifier, Designer, Planner, and Spec Reviewer. Each TOML fixes model, reasoning effort, sandbox, and its own project-input declarations. The portable protocol refers only to the abstract `project_inputs` collection; project-specific paths occur only in the relevant TOML. The adapter resolves a role's declarations immediately before that role runs and materializes only those inputs plus the named canonical artifacts into a Router-owned isolated workspace at `.router/attempts/<attempt-id>/workspace/`; canonical repository paths are never the role's writable target.

The author result union is exactly `artifact_candidate | blocking_question | upstream_finding`; the reviewer result union is exactly `review_snapshot | upstream_finding`. Framer owns only `idea.md`, Specifier only `requirements.md`, Designer only `design.md`, and Planner only `plan.md`. Spec Reviewer receives a read-only view and returns structured data through the adapter response; it never writes the review path. `upstream_finding` is valid only from a non-idea author (`drafting` or `revising`) or its current Spec Reviewer (`reviewing`) and has this closed payload: `{kind, attempt_id, invocation_hash, origin: {role, stage, status}, affected: {owner_stage, artifact_path, artifact_hash, approval_hash}, finding: {id, fingerprint, problem, evidence}, provenance: {canonical_input_hashes, project_input_manifest_hash, accepted_risk_hashes, observation_hash}, required_user_decision: {event: revise, prompt}}`. Its `id` is a stable, registry-checked `UP-NNN`; `evidence` is a non-empty structured list rather than a diagnostic string. The affected artifact path must be the fixed artifact of a strictly earlier owner stage, and the artifact hash, approval, manifest, risks, observation, attempt, invocation, role and origin checkpoint must all match the current immutable snapshot. No result variant may contain fields belonging to another variant.

Every response is written only to `.router/attempts/<attempt-id>/outbox/`, names the same `attempt_id`, and binds the invocation hash and pre-run canonical snapshot. `state.yaml`, canonical review paths, and publication are Router-only. The Router validates the result-union discriminator and every variant field before translating it to `author-valid`, `blocking-question`, `review-pass`, `review-changes-required`, or `upstream-finding`; an invalid variant is an invocation failure, never an inferred transition.

Before any launch, the Router increments a monotonic `attempt_seq` and publishes `active_attempt` with a provider-neutral identity `role:<stage>:<attempt-seq>:<invocation-hash>`, role, stage/status/counter, invocation/input hashes, isolated paths, pre-run canonical snapshot, and phase `prepared`; it then marks the phase `launched` durably immediately before calling the adapter. Attempt identities are never reused. After the adapter returns, the Router validates the current attempt identity, invocation binding, candidate boundary, and current canonical/provenance snapshot before any canonical publication. A result is publishable only when it belongs to the current attempt and the author changed only its isolated candidate output, or the reviewer changed nothing in its read-only workspace. Sandbox enforcement is defense in depth; isolated-workspace and outbox validation is authoritative. A blocking question is accepted only as a single non-empty question and causes no artifact publication.

On restart, the Router first reconciles any publication journal, then runs the ordered whole-snapshot content/provenance classifier, then performs Git-intent recovery, and only then inspects the outbox of `active_attempt`. A discrepancy fences and durably abandons any active attempt and publishes the selected reconciliation outcome before result dispatch, replacement-attempt preparation, or adapter invocation. Only a discrepancy-free snapshot may consume a complete valid current result or prepare a replacement. A complete, valid current result is then dispatched without launching another role. Otherwise `resume` durably marks that attempt `abandoned`, publishes a strictly newer prepared attempt, and only then launches the fresh role. The identity check fences the old attempt: a result arriving after abandonment or after another attempt became current is a stale result and causes no canonical write, state transition, counter change, or cleanup. An old process has no writable canonical path, so it can at most finish its own isolated outbox. Only the Router may remove attempt directories, and only after the adapter process is confirmed stopped or the attempt is both non-current and quiescent; uncertain or still-running orphans remain ignored and auditable until a later Router garbage-collection pass. Ordinary role failure records the current attempt as failed/abandoned while preserving stage/status/counter; only explicit `resume` prepares a fresh attempt.

Rationale: A narrow invocation makes context isolation testable rather than advisory. Durable attempt fencing and Router-owned workspaces make a late pre-crash agent harmless, while repository overrides preserve project-local operating choices without leaking them into the provider-neutral protocol.

### DES-003 — Validated artifact, review, and identifier contracts

Covers: REQ-018, REQ-019, REQ-020, REQ-021, REQ-022, REQ-023, REQ-024, REQ-025, REQ-026, REQ-050, REQ-052, REQ-053, REQ-054, REQ-055, REQ-056

Decision: Run structural and semantic validators before every publication. Markdown validators recognize exact stable-ID headings and required sections without requiring frontmatter. They enforce unique IDs, non-empty fields, one observable outcome per requirement or plan step, complete set coverage, and the trace chain `REQ-* -> DES-* -> STEP-*`. Plan validation also rejects a technical choice not referenced by an approved `DES-*`; final package review remains the semantic backstop for hidden redesign.

`state.yaml` keeps an append-only `id_registry` containing every published `REQ-*`, `DES-*`, `STEP-*`, and finding ID with its stage, first-seen content fingerprint, and active/tombstoned flag. Removed numbers remain tombstoned and cannot be bound to a new entity. The previous review is supplied to each fresh reviewer; the reviewer must reuse the ID for a semantically unchanged open finding, and the Router rejects duplicate or structurally malformed IDs.

Each review file is a complete latest snapshot with:

- `schema_version`, `stage`, and `verdict`;
- hashes of every artifact read;
- reviewer project-input manifest hash;
- hashes of active accepted-risk records supplied to the review;
- `findings`, each with stable `id`, `severity`, `references`, `problem`, and `recommendation`.

Allowed severities are `blocking` and `advisory`. `pass` is valid exactly when no blocking findings exist; otherwise the only valid verdict is `changes-required`. Requirements and design use stage reviews. Plan review reads and validates the complete package. Idea has no agent review. A review file is replaced only with a fully validated new snapshot.

Rationale: Small explicit validators catch mechanically observable defects, while a fresh reviewer handles semantic quality and redesign. Retaining ID tombstones and the prior review prevents identifiers from silently changing meaning across revisions.

### DES-004 — Versioned JSON-subset YAML and one deterministic helper

Covers: REQ-028, REQ-029, REQ-030, REQ-031, REQ-102

Decision: Serialize `state.yaml`, review files, manifests, risk records, intents, and adapter envelopes as UTF-8 JSON objects using sorted keys, fixed separators, and a trailing newline. This representation is valid YAML 1.2 but needs only `json` from the Python standard library. Every canonical machine record contains integer `schema_version: 1`. Readers accept known older versions only through explicit lossless migrations and stop before any write for a future or lossy version.

All `spec-id` and canonical SHA-256 work is delegated by subprocess/API call to `../../../embedded-framework/skills/sdd/scripts/sdd.py`. The helper exposes typed operations for source/spec-ID handling and for hashing text, file, and JSON-compatible values. The Router never normalizes source text, canonicalizes JSON, or computes a substitute digest; it stores and compares helper outputs. The helper itself remains Python 3 standard-library-only. Byte-identical assertions are limited to helper golden fixtures and re-reading a previously published output; independent generative role runs are compared by contract and provenance, not byte equality.

Rationale: A JSON subset avoids adding a YAML dependency while retaining the required paths and deterministic encoding. A single hashing authority prevents subtly different normalization rules from splitting provenance.

### DES-005 — Content-addressed provenance and role-local staleness

Covers: REQ-032, REQ-033, REQ-042, REQ-043, REQ-044, REQ-057, REQ-084, REQ-109

Decision: Immediately before a role run, build this manifest from only the role's TOML declaration:

```text
{
  schema_version,
  role,
  declaration_hash,
  inputs: [{path, required, missing, content_hash}]
}
```

Paths are normalized repository-relative paths; entries are sorted by path. A missing optional input has `missing: true` and no content hash. A missing required input blocks invocation. The complete manifest is stored once in `state.yaml.manifests_by_hash`; each artifact/review output record stores the manifest hash that produced it, the hashes of all canonical inputs read, active risk hashes, and the revision-input hash. Immediately before publication, the Router rebuilds the manifest and rejects the result if it changed during the run.

Approvals are immutable baseline records containing the artifact hash, required current review hash (absent only for idea), all applicable author/reviewer project-input manifest hashes, and active risk hashes. Approved upstream artifacts are mounted read-only for downstream roles.

After publication recovery and before Git recovery, outbox inspection, result dispatch, attempt preparation, or role launch, the Router computes all dependency discrepancies from one immutable repository snapshot and applies this ordered classifier. Manifest comparison is performed by the deterministic resolver/helper without materializing one role's project-input bytes into another role or into an agent context. The first non-empty class wins globally; within a class the earliest owner stage wins. All observed conditions, including lower-priority simultaneous conditions, are retained in the selected record so a guard never relies on evidence that was discarded:

1. **Approved artifact bytes changed (`manual-mutation`).** Compare every active approval's artifact hash with current bytes. Publish at the earliest changed owner stage with old/new hashes, every changed path, the approval baselines, original checkpoint/counter, the full observation snapshot, and coincident conditions. No changed artifact is called `stale-input`.
2. **Role declaration or project-input manifest changed (`stale-input`).** Only after class 1 is empty, compare declaration hash, normalized path set, required/missing bits, and content hashes. Old and new full manifests must differ. Publish at the earliest affected stage with role, declaration hashes, old/new manifests and their hashes, affected stages, required-input readability, original checkpoint/counter, full snapshot, and coincident lower-priority conditions.
3. **Review provenance mismatch (`review-invalidation`).** Only after classes 1 and 2 are empty, a changed review file, artifact input, reviewer manifest edge, or accepted-risk input deactivates that review and its dependent risks/approvals. Preserve the review bytes and evidence, publish the earliest affected non-idea stage in `reviewing` with a new fenced reviewer attempt, and retain the counter. This is an automatic invalidation/re-review route, not `stale-input` and not acceptance of externally changed review bytes.
4. **Accepted-risk provenance mismatch (`risk-invalidation`).** Only after classes 1--3 are empty, deactivate each risk whose review, finding fingerprint, or provenance edge no longer matches and invalidate dependent approvals. If this exposes any current blocking finding, publish `awaiting-decision/unresolved-findings` at the earliest affected stage with risk/review/finding hashes, exposed IDs, original checkpoint/counter, full snapshot, and coincident conditions. If no blocking finding is exposed, return that stage to `awaiting-approval` with the inactive risks omitted from its baseline. Existing risk records remain append-only.

A reconciliation-produced pending record additionally stores `classifier_version`, selected class/rank, observation hash, and `all_conditions`; re-running against the same snapshot is a no-op. Before publishing any non-empty-class outcome, the Router marks the current attempt fenced/abandoned in the same transaction, stores its identity in reconciliation evidence, and makes `active_attempt` null. A new higher-priority or earlier condition transactionally supersedes the pending while retaining its evidence. Any non-reconciliation pending displaced by repository truth is retained as `suspended_context` but is not independently actionable. Resolution always reruns publication recovery and this classifier first, and then requires the current snapshot to equal the record's observation except for the user repairs explicitly allowed by that pending. Thus simultaneous conditions are neither hidden nor accidentally consumed.

Resolving `stale-input` requires all required inputs to be readable, the recomputed manifest to differ from the saved old manifest, and that new manifest to remain byte-for-byte stable from guard evaluation through transition publication. The new manifest and feedback are stored, pending is cleared, dependent invalidations remain, the counter becomes zero, and a new fenced author starts at the earliest affected stage (`drafting` for idea, otherwise `revising`). A failed guard changes nothing and never retries automatically.

Rationale: Content-addressed manifests give precise, role-local invalidation. An ordered whole-snapshot classifier prevents artifact mutation, project-input staleness, review invalidation, and risk invalidation from being conflated when they occur together.

### DES-006 — Append-only accepted-risk records

Covers: REQ-045, REQ-046, REQ-047, REQ-048, REQ-049, REQ-070, REQ-071

Decision: Store accepted risks as an append-only content-addressed map in `state.yaml.accepted_risks_by_hash`. A record contains `schema_version`, stage, finding ID, finding fingerprint, review hash, hashes of every review provenance input, exact user decision, and optional comment. Editing a decision creates a new record; old records remain auditable.

A risk is active only when its review, finding fingerprint, and every provenance hash still match. Active upstream risks are included verbatim in dependent author envelopes and by hash plus content in reviewer envelopes. Their hashes appear in resulting provenance and approvals. Any changed review, review input, or finding text deactivates the old record without deleting it.

For `unresolved-findings`, `accept-risk(ids)` is one Router transaction: resolve IDs against the current review, create records for all current blocking findings not already actively accepted, validate the resulting complete set, then move to `awaiting-approval`. Partial, unknown, or stale sets leave state unchanged. `continue` and `continue-and-commit` share a guard that requires a valid current artifact/review/manifests and no unaccepted current blocking finding.

Rationale: Append-only risk records preserve the user's exact exception and its baseline. Treating risks as provenance inputs prevents an old waiver from silently applying after the underlying evidence changes.

### DES-007 — Data-driven exhaustive state machine

Covers: REQ-058, REQ-059, REQ-060, REQ-061, REQ-062, REQ-063, REQ-064, REQ-065, REQ-066, REQ-067, REQ-068, REQ-069, REQ-070, REQ-071, REQ-072, REQ-073, REQ-074, REQ-075, REQ-076, REQ-077, REQ-078, REQ-079, REQ-085

Decision: Appendix A is the normative, machine-readable transition oracle. It defines legal state shapes, the recovery/reconciliation/event precedence, wildcard matching, an action schema with defaults for every omitted field, phase consume/continue behavior, every guarded success and failure effect, counter mutation, publication and Git outcomes, attempt preparation/invocation success and failure, checkpoint actions, stop/terminal behavior, and the immutable invalid-event default. Prose in DES-005 and DES-008--DES-010 explains the records and algorithms, but Appendix A wins if prose could otherwise be read more broadly.

Legal active-role states are `idea/drafting`, `requirements|design|plan/drafting`, `requirements|design|plan/revising`, and `requirements|design|plan/reviewing`. Checkpoints are any stage in `awaiting-approval`; decision states require exactly one stage-compatible pending kind; `approved` is legal only as `plan/approved`; `aborted` is legal at any stage and has no active attempt. `active_attempt` is optional only in an active-role state: it is present while prepared/launched work may still return, and absent after an ordinary failure until explicit `resume`. No other stage/status/pending shape passes schema validation.

The counter is the number of automatic revision attempts already entered. It starts at zero; the first failed review enters attempt 1, and a failed review at counter three stops without attempt four. User-directed revision resets it to zero. `question`, `stop`, clarification, ordinary failures, boundary resolution, reconciliation, and commit failures do not consume an attempt. A transition that launches a role first publishes the target state and a new fenced `active_attempt`, then performs at most one adapter call.

Every `answer-clarification` transition publishes the same stage with status `drafting`, regardless of whether the question originated in `drafting` or `revising`. The durable revision input supplied to the new author retains the originating stage/status, current counter and automatic-revision findings/context, the exact question, the exact answer, and their hashes. The counter is unchanged. This is the sole clarification target.

Rationale: A closed executable oracle makes every valid and invalid combination testable. Publishing state and attempt identity before a launch guarantees that restart and late-result handling cannot select a different transition.

### DES-008 — Explicit exceptional decisions and downstream invalidation

Covers: REQ-080, REQ-081, REQ-082, REQ-083, REQ-084, REQ-085, REQ-086, REQ-087, REQ-108, REQ-109, REQ-110

Decision: Extend the same transition table with typed pending records. Every pending record includes `schema_version`, `kind`, owning/target stage, originating stage/status/counter, evidence object and its canonical hash, allowed resolution events, suspended revision/clarification context, active-attempt identity if the decision followed a run, and the complete precondition snapshot needed by every resolution guard after restart. Evidence never consists only of a diagnostic string.

| Pending kind | Creation | Allowed mutating resolution and exact effect |
|---|---|---|
| `clarification` | A current author attempt returns exactly one valid question. Save attempt/input identity, originating author status, counter, revision context/findings, exact question and hash; clear `active_attempt`. | `answer-clarification(answer)` requires a non-empty answer and unchanged stored inputs. Save the exact answer/hash, clear pending, keep counter, publish the same stage as `drafting`, retain question/answer/originating revision context as role inputs, prepare and launch one fresh author. |
| `upstream-revision` | A validated `upstream_finding` from the current non-idea author or reviewer reports a material finding owned by a strictly earlier approved stage. Save the complete adapter payload, role/status/attempt/invocation binding and observation provenance; do not alter upstream bytes. | `revise(feedback)` invalidates approvals/reviews/risks at the owner and downstream, preserves downstream files as stale, resets counter, and enters owner `drafting` for idea or `revising` otherwise. |
| `manual-mutation` | Class 1 reconciliation finds approved artifact bytes different from baseline. Save every old/new hash and path, owner approvals, original checkpoint/counter, observation hash, and all simultaneous conditions; accept neither the edit nor a rollback. | `revise(feedback)` only if every still-changed approved path belongs to the selected owner artifact, every recorded path/hash is accounted for, and no other unreconciled condition exists. Preserve user bytes as new revision input, store hashes/feedback, invalidate owner/downstream baselines, reset counter, and enter the owner author state. |
| `stale-input` | Class 2 reconciliation finds a changed role declaration or manifest. Save differing old/new complete manifests and hashes, declaration hashes, required-input readability, affected stages, original checkpoint/counter, observation hash, and all simultaneous conditions. | The guarded resolution is exactly DES-005. An old/new pair with equal manifest hashes is schema-invalid. |
| `boundary-violation` | Current-attempt workspace/outbox validation finds a forbidden output or canonical-write attempt. Save attempt/invocation identity, originating role/status, all changed isolated or canonical paths, pre-run hashes, allowed-input provenance, observation hash, and diagnostic; reject the candidate and clear `active_attempt`. | `retry-role` only after every forbidden canonical path matches its pre-run hash, the attempt remains the pending's origin, and allowed provenance is unchanged. Clear pending, restore originating state, keep counter, prepare a newer attempt, then launch exactly one fresh same-role agent. |
| `commit-conflict` | Git preparation/execution or recovery diverges from its intent. | `cancel-commit`, `retry-commit`, and `revise(feedback)` only as defined by DES-010. |

Stale downstream files and review snapshots are never deleted merely because their provenance is inactive. State carries active/inactive status, so audit evidence remains while guards reject stale data. Failed resolution guards are read-only and require a new explicit event. `question`, `stop`, and `abort` remain the only common checkpoint/terminal actions stated in Appendix A; no pending kind inherits another kind's mutating resolutions.

Rationale: Typed pending records turn anomalies into resumable user decisions. Keeping bytes while invalidating baselines avoids data loss and separates evidence preservation from approval validity.

### DES-009 — Recoverable multi-file publication

Covers: REQ-093, REQ-094, REQ-095, REQ-096

Decision: Serialize all mutations for a spec with a per-spec lock and a write-ahead transaction under `.stepan/specs/<spec-id>/.router/txn/<operation-id>/`. The transaction stores canonical JSON metadata, candidates, before-images when a canonical path already exists, source logical state hash, target hashes, target logical state, and any consuming/current role attempt identity. Role candidates can enter a transaction only by Router copy from the validated current attempt outbox; an agent never receives a canonical transaction path. Router-owned transaction and attempt paths are never included in package approvals or Git checkpoints.

Publication phases are:

1. validate the candidate, schema, provenance, guards, and write boundary without touching canonical paths;
2. write and sync candidate/before-image files and transaction metadata;
3. atomically replace `state.yaml` with the same logical checkpoint plus `operation: {id, kind, phase: prepared, source_state_hash, target_state_hash, payload_hashes, attempt_id}` and sync it;
4. atomically replace each canonical payload, then re-read and hash it through the helper;
5. atomically replace `state.yaml` with the exact target logical state and no active operation;
6. remove only that Router-owned transaction directory.

Atomic replacement uses same-directory temporary files, `os.replace`, file flush/`fsync`, and directory sync where the platform supports it. Initialization of `state.yaml` is a single atomic write; no role starts until it can be re-read and validated.

On resume, publication reconciliation runs first. Its exhaustive outcomes are: `target-complete` finalizes the stored target; `source-intact` restores the exact source checkpoint; `subset-completable` finishes only from all validated durable candidates; `subset-restorable` restores all before-images; and `integrity-failure` performs no canonical write and stops dispatch with a diagnostic. After a successful publication-recovery outcome, the fresh whole-snapshot content/provenance classifier runs before Git recovery, role outbox handling, result consumption, attempt preparation, or event dispatch. Target completion consumes only the exact `attempt_id` named by the operation, and only after that classifier finds no discrepancy; every other attempt result is stale. A missing or mismatched candidate is never treated as output. Reconciliation decisions depend only on the journal and current repository hashes, so applying them twice gives the same result.

Rationale: Filesystem atomicity is available per replacement, not across a package. A small write-ahead protocol makes every intermediate shape recognizable and allows already published generative work to be reused safely.

### DES-010 — Isolated explicit Git approval checkpoints

Covers: REQ-088, REQ-089, REQ-090, REQ-091, REQ-092, REQ-097, REQ-098, REQ-099, REQ-100, REQ-101, REQ-111, REQ-112, REQ-113

Decision: Git is optional and is touched only by explicit `continue-and-commit`, `retry-commit`, or recovery of their durable intent. `continue-and-commit` first applies exactly the same approval guard as `continue`, before any Git object, ref, or index write. The Router then snapshots `HEAD`, symbolic/detached ref identity, the real index bytes/hash and parsed logical entries, package/worktree hashes, and the staged delta from `HEAD`.

The Git-specific isolation guard rejects unmerged entries, unsupported index extensions/layouts that cannot be replayed losslessly, and any pre-existing staged delta at any path below `.stepan/specs/<spec-id>/`. A rejection is routed to `commit-conflict` with exact overlapping paths or unsupported features; nothing is unstaged or guessed. Stage-0 additions, modifications, deletions, modes, and supported index flags outside that package are permitted. Unstaged, untracked, ignored, and hook-created files are permitted everywhere if the eight canonical paths still have the validated bytes. Before any Git mutation, the Router durably publishes a commit intent containing all snapshots and guard evidence, source stage/status/counter, attempt ID, expected package tree, exact message `sdd(<spec-id>): approve <stage>`, target approval transition, saved non-package staged delta, hashes of both alternate indexes described below, and phase `prepared`.

The journaled operation uses two Router-owned indexes:

1. Build an **approval index** from the saved old `HEAD` tree. Replace entries only for the eight canonical package paths that exist in the validated target package, using the approved artifacts/reviews and target `state.yaml`; remove absent canonical paths; exclude `.router/` and all other paths. Write the expected tree and single-parent commit object, then verify parent, tree, package-only diff, and byte-exact message against the intent.
2. Before moving the ref, build a **candidate real index** from the expected new commit tree and replay the saved logical staged delta for every permitted non-package path, including deletions, modes, and supported flags. Validate that its diff from the expected new `HEAD` is exactly the saved non-package staged delta and has no package entry. This preserves staging disposition, not old index bytes.
3. Recheck that `HEAD`, ref identity, real-index hash/logical entries, validated package bytes, and protected operation inputs still equal the intent. On any mismatch, publish `commit-conflict` and do not update the ref or real index.
4. Compare-and-swap the saved ref from old `HEAD` to the verified commit and journal phase `ref-updated`. A CAS failure cannot move the ref and becomes `commit-conflict`; an unreachable commit object may remain but is not canonical history.
5. If and only if CAS succeeded, copy the validated candidate real index to `.git/index.lock`, sync it, verify the lock hash, atomically replace the real index, sync its directory, and journal phase `index-reconciled`. The saved real-index before-image remains in the intent until completion. No hook runs.
6. Verify reachable commit, real-index semantics, preserved non-package staged delta, package/worktree bytes, and untouched non-Router paths. Then publish the stored target state through DES-009, journal `state-published`, and complete the intent.

The only intentional changes are the approval commit/ref, package `state.yaml` target publication, and replacement of the real index with the semantically reconciled candidate. Non-package index entries retain their previous staged delta; package entries match the new HEAD and therefore cannot appear as staged reversals. Existing unstaged/untracked/ignored/hook-created bytes are never added, removed, reverted, or committed. Index byte identity is neither promised nor tested after success; logical staging identity outside the package and absence of a package staged delta are required.

Every phase is recoverable from the durable intent:

- Before CAS, old `HEAD` plus matching saved real index and guards is `not-created`: mark the attempt completed with its diagnostic, keep the original `awaiting-approval` state/counter, and require a new explicit event. An already built unreachable object is harmless.
- After CAS but before index reconciliation, if the real index still equals the saved before-image, install the already validated candidate index and continue. If it already equals the candidate, continue. Any third index shape becomes `commit-conflict`; recovery never overwrites concurrent staging.
- After index reconciliation, verify the exact reachable commit and candidate semantics, then publish the target if needed. A target already published is finalized without a second commit.
- Any HEAD/ref, package, candidate-index, real-index, or worktree shape outside those cases becomes the same idempotent `commit-conflict`, recording phase, before/expected/observed snapshots, overlapping paths, intended target, attempt identity, and whether HEAD already moved. Recovery never resets HEAD, restores the index before-image over user changes, or alters non-Router worktree bytes.

Thus the closed outcomes are `verified-success`, `not-created`, `ordinary-failure` (no commit, stable snapshot), and `commit-conflict`. Verified success publishes the same target as `continue`, clears pending, completes intent, and resets counter. `not-created` and ordinary failure complete the attempt with a diagnostic, keep `awaiting-approval` and the counter, and never retry automatically. Conflict keeps the source approval stage in `awaiting-decision/commit-conflict` with the counter unchanged even if the exact commit already moved HEAD but index reconciliation was contested.

From `commit-conflict`:

- `cancel-commit` requires current approval guards and removable Router-only temporary data; it marks the intent `cancelled`, retains conflict evidence, clears pending, and returns to the same `awaiting-approval` checkpoint without changing counter, current HEAD, current real index, or non-Router worktree bytes;
- `retry-commit` requires approval guards and a current snapshot from which an exact package-only commit and candidate real index can be isolated. It stores a new intent and performs exactly one attempt. If HEAD already names the prior exact expected commit, the attempt may reconcile/verify that commit without creating a second one; otherwise it creates the one expected commit. It ends only in verified success, ordinary failure/not-created, or a new conflict;
- `revise(feedback)` requires that Router temporary operations can be abandoned; it marks the intent `abandoned`, retains evidence/feedback, clears pending, resets counter, and enters same-stage `drafting` for idea or `revising` otherwise, without changing current HEAD/index/non-Router worktree bytes.

Each failed guard leaves pending, intent, counter, HEAD, index, and worktree unchanged. Cleanup removes only paths named Router-owned in the intent, never `.git/index` or its saved before-image until the intent is terminal and verified.

Rationale: The approval index isolates the commit, while a separately validated post-commit index preserves the user's non-package staged delta relative to the new HEAD. Journaling both CAS and index replacement makes crashes safe without claiming that obsolete index bytes can remain unchanged.

### DES-011 — Model, crash, and adapter conformance suites

Covers: REQ-103, REQ-104, REQ-105

Decision: Maintain one provider-neutral golden-scenario suite. The fake runner and Codex adapter execute the same scenarios for stage ordering, guards, provenance, accepted risks, write boundaries, all table transitions, and recovery. A model-based test enumerates every schema-valid `(stage, status, pending kind, event)` plus invalid events and compares observed hashes/effects with the declarative transition row.

Inject process termination during every author/reviewer run and after each durable boundary: attempt prepared, attempt marked launched, current outbox completed, transaction candidate, prepared publication intent, each canonical replace, target state, approval, approval-index creation, candidate-real-index creation, commit object creation, ref CAS, real-index lock sync/swap, state publication, and intent completion. Each fixture resumes in a new host process and must converge to the uninterrupted checkpoint without duplicated role output or commit.

Role-race fixtures keep the pre-crash adapter alive. In the first fixture the old agent completes before resume: resume consumes its valid current outbox and launches no replacement. In the second it completes after resume has durably abandoned it and published a newer attempt: both old-before-new and old-after-new completion orders must reject the old result, publish only the current result, leave canonical paths inaccessible to both agents, and permit Router-only cleanup only after the old process is quiescent. A third fixture crashes after preparing but before starting the adapter and proves that resume may rotate the unused identity without accepting any later result for it.

Codex-specific conformance adds parent/prior-context canaries, author sentinel writes, read-only reviewer checks, attempt/outbox identity tampering, and late-agent races. Passing fake and Codex runners proves the first adapter and protocol separation. Portability to a second real agent system is claimed only after that adapter passes the identical golden suite; fake-runner success is explicitly insufficient evidence.

Rationale: The design's hardest properties are closed transitions and recovery behavior, which are better proven by generated state/event cases and crash injection than by selected happy-path tests.

## Transition invariants

The Router enforces these invariants before and after every durable mutation:

- exactly one active spec lock and at most one active `operation`/pending commit intent per package;
- at most one current `active_attempt`; only a result bound to that identity and invocation can enter a publication transaction;
- stage order never advances without a hash-bound approval of the preceding stage;
- canonical artifact and review hashes equal helper results and all active provenance edges resolve;
- an approval is active only if its complete baseline still matches;
- only the latest review snapshot is active, while historical risk and ID records remain append-only;
- a role result cannot alter Router-owned files or an upstream artifact;
- `approved` means exactly `plan/approved`; `aborted` has no mutating exit;
- no role launch occurs until the state that requires it is durably readable;
- roles can write only their Router-owned isolated workspace/outbox; abandoned or late attempts cannot mutate canonical files;
- no automatic retry follows a role failure, failed resolution guard, Git failure, or commit conflict;
- reconciliation order is publication recovery, the ordered whole-snapshot content/provenance classifier, Git recovery, then current-attempt outbox recovery; a discrepancy fences the active attempt and publishes its outcome before any result consumption, attempt preparation, or launch, and every phase is idempotent for an unchanged repository snapshot;
- after successful approval commit, the real index has no package staged delta and has exactly the saved non-package staged delta relative to new HEAD.

## Affected components

| Component | Responsibility | Write authority |
|---|---|---|
| Portable protocol | Schemas, role/result types, dependency graph, transition rows, adapter scenarios | Maintainer at implementation time; never a runtime role |
| Router CLI/core | Event dispatch, guards, validation, role orchestration, provenance, state transitions | Canonical state, reviews, accepted risks, approvals, Router transaction data |
| `../../../embedded-framework/skills/sdd/scripts/sdd.py` | Authoritative spec-ID generation and canonical hashes | No package mutation; returns deterministic values |
| Adapter interface | Fresh-context role execution, attempt fencing, and capability mapping | Only the current Router-owned isolated workspace/outbox permitted by invocation |
| `.codex/agents/*.toml` | Repository-specific role prompt, project inputs, model, reasoning, sandbox | Repository configuration; read-only during a flow |
| Artifact validators | Markdown contracts, stable IDs, coverage and traceability | None |
| Review validator | Review schema, verdict/finding consistency and provenance | None |
| Publication/recovery coordinator | Per-spec lock, attempt/outbox recovery, write-ahead transaction, atomic replacements, reconciliation | `.router/` attempt/transaction data and Router-owned canonical publications |
| Git checkpoint coordinator | Approval index, candidate real index, commit intent, exact commit/ref/index verification | Router temp indexes, Git objects, one CAS ref update, and one journaled real-index replacement on explicit request |
| Conformance harness | Model-based, crash-matrix, fake-runner and adapter tests | Test fixtures only |

## Resolved constraints

| Fixed constraint | Resolution | Requirement/design mapping |
|---|---|---|
| Repository-scoped agent overrides remain the source of role settings. | Separate `.codex/agents/*.toml` files remain mandatory and own project inputs, model, reasoning, and sandbox settings. | REQ-038, REQ-039; DES-002 |
| The common protocol prescribes no global project-document paths. | Protocol exposes only a role-local `project_inputs` abstraction; concrete paths exist solely in that role's TOML and are resolved at invocation time. | REQ-040, REQ-041, REQ-042; DES-002, DES-005 |
| Spec-ID and canonical hash algorithms remain in `../../../embedded-framework/skills/sdd/scripts/sdd.py`. | Router invokes typed helper operations and stores their outputs; it contains no normalization, canonicalization, suffixing, or hashing implementation. | REQ-030, REQ-031, REQ-034, REQ-036, REQ-037; DES-001, DES-004 |
| The canonical package location and names are interoperability contracts. | All version-controlled flow data is under `.stepan/specs/<spec-id>/` at the eight fixed paths; `.router/` is temporary and excluded from commits. | REQ-015, REQ-016, REQ-017; DES-001, DES-009, DES-010 |
| The flow ends before implementation. | The only successful terminal state is `plan/approved`; no implementation role or transition exists. | REQ-002, REQ-004, REQ-058, REQ-073; DES-001, DES-007 |
| Idea has a user checkpoint but no agent review. | Framer publication moves directly to `idea/awaiting-approval`; required reviews begin at requirements. | REQ-050, REQ-061; DES-003, DES-007 |
| Authored artifacts are Markdown and service files are versioned YAML. | Markdown stays frontmatter-free; `.yaml` machine files use deterministic JSON-subset YAML with `schema_version`. | REQ-018, REQ-019, REQ-022, REQ-024, REQ-028; DES-003, DES-004 |

## Constraints, risks and trade-offs

- **Python/runtime:** deterministic core behavior uses Python 3 standard-library facilities. Git checkpointing also requires a compatible Git CLI because commits cannot be implemented by the Python standard library alone.
- **JSON-subset YAML:** this sacrifices YAML comments and richer syntax but removes a parser dependency, makes canonical encoding simple, and still satisfies YAML tooling that accepts YAML 1.2 JSON documents.
- **Filesystem guarantees:** `os.replace` is atomic only on the same filesystem. Directory `fsync` is best-effort on platforms that do not expose it; the journal and before-images remain the correctness mechanism.
- **Repository scans:** authoritative post-role boundary checks and worktree conflict detection can be expensive in very large repositories. Implementations may cache unchanged directory metadata, but publish-time decisions must ultimately use helper-derived content hashes for every relevant changed or protected path.
- **No Git hooks for approval commits:** plumbing avoids hook side effects and preserves worktree bytes and the logical non-package staged delta, at the cost of not running repository commit hooks. Successful approval changes the real index bytes so package entries match new HEAD; this replacement is guarded and journaled. Required policy checks should run as explicit Router guards; a future hook mode must retain the same isolation and recovery contract.
- **Concurrent activity:** unrelated repository changes are allowed, but changes to HEAD, the real index, canonical package paths, or paths captured by the active operation turn a commit into an explicit conflict. Pre-existing staged changes below the package root are also a conflict; staged changes elsewhere are replayed. The Router never guesses ownership or rolls back such bytes.
- **Semantic identity:** stable requirement/design/step and finding identity cannot be proven entirely by syntax. The registry, tombstones, prior review input, and reviewer checks make reassignment visible; human review resolves genuinely semantic ambiguity.
- **Machine-state growth:** full manifest history and append-only risks increase `state.yaml` over time. This is accepted for auditability in pre-development packages; a future schema may move content-addressed history to separate canonical files only through a lossless migration.

## Verification

Verification is layered and all layers must pass:

1. **Schema/contract fixtures:** valid and invalid examples for every artifact, review, state version, manifest, accepted-risk record, pending kind, ID registry rule, coverage relation, and verdict combination.
2. **Helper golden fixtures:** source/spec-ID collisions and canonical hashes execute in a clean Python 3 environment; direct helper and Router-mediated results are identical, while static inspection confirms no Router duplicate algorithm.
3. **Transition model:** compile Appendix A, enumerate all valid stage/status/pending/event/guard combinations and all unspecified events, and compare target, durable effects, counter, attempt identity/role-launch count, recovery action, and retry behavior with its first-match rule. Unspecified rows must preserve all canonical hashes.
4. **Provenance scenarios:** mutate every artifact, project-input content/path/presence/declaration, review input, finding text, and risk record dependency independently. Confirm earliest-stage invalidation, stale downstream preservation, active-risk propagation, and approval rejection.
5. **Role-boundary scenarios:** use parent-context canaries, other-role input canaries, forbidden author sentinels, reviewer writes, malformed questions, wrong/stale attempt IDs, and crashes/failures. Confirm fresh contexts, isolated outboxes, no unauthorized publication, late-result rejection, and explicit retry only.
6. **Publication crash matrix:** terminate after every phase in DES-009, resume twice, and compare the final package hashes/checkpoint with an uninterrupted control. Published valid generative outputs must be reused.
7. **Git matrix:** test clean and dirty worktrees, staged non-package additions/modifications/deletions/modes/flags, staged package overlap, unsupported/unmerged indexes, unstaged/untracked/ignored and pre-existing hook-created files, detached/symbolic HEAD as supported, ordinary failure, external ref movement, index/package mutation, and every crash point through CAS/index swap. Verify exact package-only diff/message, one reachable commit on success, exact replayed non-package staged delta relative to new HEAD, no package staged reversal, untouched worktree data, and conflict resolution effects.
8. **End-to-end golden scenarios:** run the full `start-new -> idea -> requirements -> design -> plan/approved` flow plus revision, accepted-risk, upstream-return, manual-mutation, stale-input, boundary-violation, abort, and each commit-conflict resolution through fake and Codex runners.
9. **Final package audit:** from a new process with no conversation, use repository files alone to recover `plan/approved`, find four non-empty artifacts, current required reviews and approvals, and prove complete `REQ-* -> DES-* -> STEP-*` traceability with no provider-specific canonical identifiers.

## Appendix A — Normative transition oracle

This fenced YAML is normative. Lists in a selector mean set membership, `*` means every value in the named domain, `$field` reads a durable record field, and `same`/`keep` mean byte-for-byte unchanged. Rules are evaluated by ascending `priority`; the first selector match is final. Its guard is then evaluated exactly once: `pass` applies `success`, while `fail` applies that row's `failure`. Recovery and reconciliation rules run before event rules. The last rule therefore exhaustively maps every otherwise valid combination to an immutable invalid event.

```yaml
oracle_version: 2
domains:
  stage: [idea, requirements, design, plan]
  status: [drafting, reviewing, revising, awaiting-approval, awaiting-decision, approved, aborted]
  pending_kind: [null, clarification, unresolved-findings, upstream-revision, manual-mutation, stale-input, boundary-violation, commit-conflict]
  external_event: [start-new, resume, answer-clarification, revise, accept-risk, continue, continue-and-commit, question, stop, abort, retry-role, cancel-commit, retry-commit]
  internal_event: [inspect-current-outbox, author-valid, blocking-question, upstream-finding, review-pass, review-changes-required, role-failure, boundary-violation, git-verified, git-not-created, git-ordinary-failure, git-conflict]

legal_states:
  - {stage: idea, status: drafting, pending: null, role: author}
  - {stage: [requirements, design, plan], status: [drafting, revising], pending: null, role: author}
  - {stage: [requirements, design, plan], status: reviewing, pending: null, role: reviewer}
  - {stage: "*", status: awaiting-approval, pending: null, role: null}
  - {stage: "*", status: awaiting-decision, pending: [clarification, upstream-revision, manual-mutation, stale-input, boundary-violation, commit-conflict], role: null}
  - {stage: [requirements, design, plan], status: awaiting-decision, pending: unresolved-findings, role: null}
  - {stage: plan, status: approved, pending: null, role: null}
  - {stage: "*", status: aborted, pending: null, role: null}
state_constraints:
  - pending_is_non_null_iff_status_is_awaiting_decision
  - idea_clarification_origin_status_is_drafting
  - nonidea_clarification_origin_status_is_drafting_or_revising
  - upstream_revision_owner_precedes_origin_stage
  - commit_conflict_origin_status_is_awaiting_approval
  - boundary_violation_origin_status_is_a_legal_role_status
  - active_attempt_is_null_unless_role_is_author_or_reviewer
  - active_attempt_identity_equals_role_colon_stage_colon_monotonic_attempt_seq_colon_invocation_hash
  - operation_and_commit_intent_are_mutually_exclusive
  - scheduled_launch_is_null_unless_state_role_is_author_or_reviewer
  - scheduled_launch_and_active_attempt_are_mutually_exclusive

semantics:
  phase_order: [publication-recovery, content-reconciliation, git-recovery, scheduled-launch, current-attempt-outbox, event-dispatch, invalid-default]
  snapshot: publication recovery completes first; content reconciliation then uses one fresh immutable helper-hashed repository/index/HEAD snapshot; no later phase may use a different snapshot without restarting at publication-recovery
  action_defaults: {ref: null, target: same, canonical_writes: [], git_writes: [], counter: keep, active_attempt: keep, pending: keep, commit_intent: keep, dispatch: none, launch: none, launch_profile: null, adapter_calls: zero, emit: none, retry: none, host: none, diagnostic: none, event_behavior: consume, phase_behavior: consume}
  action_field_meanings:
    ref: null or one key in action_templates; resolve that template over action_defaults before applying any explicit fields, with no recursive references
    target: exact resulting stage/status/pending, or same for no change
    canonical_writes: ordered Router-owned durable operations and no others
    git_writes: ordered Git operations and no others
    counter: one of keep, zero, increment-one, source, or target
    active_attempt: one of keep, null, source, target, prepared, or launched
    pending: one of keep, null, target, or the named pending record
    commit_intent: one of keep, null, prepared, completed, cancelled, abandoned, or isolation-rejected
    dispatch: none or the validated bound event named by the action
    launch: none or scheduled-one; scheduled-one evaluates the declared profile condition from the already-computed target, acts as none if false, otherwise durably writes that profile as scheduled_launch, consumes the current pass, and restarts at publication-recovery
    adapter_calls: zero or one; only attempt_execution may set one
    emit: none, inspect-current-outbox, or exactly-one-git-outcome; exactly-one-git-outcome is exactly one of git-verified, git-not-created, git-ordinary-failure, or git-conflict produced by the one journaled Git attempt
    retry: none, explicit-resume-only, or explicit-event-only
    host: an output-only diagnostic/checkpoint action that cannot mutate durable or Git state
    diagnostic: none or the exact diagnostic class stored/emitted by the action
    event_behavior: consume discards the current event; keep carries it; replace-with-dispatch replaces it with dispatch
    phase_behavior: consume ends phase evaluation and returns after durably scheduling any launch; continue-next advances to the next phase; continue-to-event-dispatch jumps to event-dispatch
  phase_rules:
    - a consume action cannot execute any later phase in the same pipeline pass
    - a continue action applies its durable effects before advancing and carries or replaces the event exactly as event_behavior states
    - scheduled-one stores the profile and target observation as scheduled_launch, ends the pass, and restarts at publication-recovery; only a later discrepancy-free scheduled-launch phase may enter attempt_execution, so a fresh whole-snapshot classifier always precedes preparation
    - after an adapter call returns, attempt_execution restarts at publication-recovery with event inspect-current-outbox; content reconciliation therefore runs again before result validation or dispatch
    - emit exactly-one-git-outcome restarts at publication-recovery with the one emitted Git event; content reconciliation therefore reruns before T422, T423 or T424 consumes that outcome
    - guard failure uses all action defaults plus diagnostic guard-failed; invalid default uses all action defaults plus diagnostic invalid-event
    - selector fields approval_guard, git_isolation_guard and origin_role_status are pure values computed once from the current reconciled snapshot using guard_definitions before priority matching; they perform no writes
  launch: attempt_execution is the only mechanism that can prepare an active attempt or call an adapter
  late_result: any_internal_role_event_whose_attempt_id_is_not_current_uses_rule_T400
  reconciliation_precedence: [approved-artifact-mutation, project-input-change, review-provenance-mismatch, accepted-risk-provenance-mismatch, none]
  simultaneous_tie_break: class_rank_then_earliest_stage_then_lexical_role_and_path

action_templates:
  guard_failure: {diagnostic: guard-failed}
  invalid_event: {diagnostic: invalid-event}

derived_values:
  target_expression: slash joins a resolved stage, status and pending value; omitted pending is null; every resolver used below is declared in this map
  same_stage: current stage
  next_stage: successor in idea-to-requirements-to-design-to-plan
  owner_stage: affected.owner_stage from a validated upstream-finding or pending evidence
  earliest_affected: earliest stage selected by the classifier
  earliest_changed_owner: earliest owner stage selected by approved-artifact-mutation
  saved_stage: pending.origin.stage
  saved_origin_status: pending.origin.status
  source_stage: commit_intent.source.stage
  target: exact state stored in the recovered publication or commit intent
  idea_drafting_else_same_stage_revising: idea/drafting/null for idea, otherwise current-stage/revising/null
  owner_idea_drafting_else_owner_revising: owner-stage/drafting/null for idea owner, otherwise owner-stage/revising/null
  earliest_affected_idea_drafting_else_revising: earliest-affected/drafting/null for idea, otherwise earliest-affected/revising/null
  plan_approved_else_next_stage_drafting: plan/approved/null for plan, otherwise next-stage/drafting/null
  author-for-current-stage: Framer for idea, Specifier for requirements, Designer for design, or Planner for plan
  author-for-target-stage: author-for-current-stage evaluated at target.stage
  author-for-next-stage: author-for-current-stage evaluated at next_stage
  author-for-owner-stage: author-for-current-stage evaluated at owner_stage
  author-for-earliest-affected-stage: author-for-current-stage evaluated at earliest_affected
  saved-current-role: active_attempt.role when present, otherwise the role implied by the current legal role state
  idea-drafting-else-revising: drafting for idea and revising otherwise
  target.status-is-drafting: true exactly when the recovered target status is drafting
  source_stage-is-not-plan: true exactly when commit_intent.source.stage is idea, requirements, or design

guard_definitions:
  unique_or_explicitly_chosen_helper_id_and_valid_source: source payload is nonempty and helper result is unused, or the user explicitly chose the helper-returned lowest free suffix; no package exists at the selected path
  valid_Framer_candidate_and_unchanged_inputs: current Framer-bound artifact_candidate passes idea/boundary/attempt/provenance validation and the post-run snapshot equals its invocation snapshot
  valid_owned_candidate_boundary_and_unchanged_inputs: current author-bound artifact_candidate owns exactly the stage artifact, passes its artifact/boundary/provenance validators, and the post-run snapshot equals its invocation snapshot
  exactly_one_nonempty_question_and_legal_author_status: current author-bound blocking_question has exactly one nonempty question and no fields from another result variant
  nonempty_answer_and_unchanged_pending_inputs: answer is nonempty and all hashes saved by the clarification pending equal the current reconciled snapshot
  valid_current_review_with_no_blocking_findings: current reviewer-bound review_snapshot passes review/boundary/provenance validation and has verdict pass with zero blocking findings
  valid_current_review_with_blocking_findings: current reviewer-bound review_snapshot passes review/boundary/provenance validation and has verdict changes-required with at least one blocking finding
  adapter_failure_before_valid_publication: current attempt has a typed invocation or result-validation failure record and no candidate entered a publication transaction
  complete_boundary_evidence: current attempt evidence lists every observed changed path, before/after helper hash, allowed path set, invocation and snapshot binding
  valid_upstream_finding: result is the closed upstream_finding variant; origin role is the current non-idea author iff status is drafting/revising or current reviewer iff status is reviewing; owner stage strictly precedes origin stage; affected path is that owner's fixed artifact; its hash and approval are current; UP-NNN identity/fingerprint, nonempty evidence, all provenance hashes and required revise decision validate against the reconciled snapshot
  nonempty_feedback_and_current_checkpoint: feedback is nonempty and artifact/review/approval/manifests/risks still equal the awaiting-approval snapshot
  nonempty_feedback_and_current_pending: feedback is nonempty and all unresolved-finding IDs and provenance equal the current review snapshot
  exact_complete_current_blocking_ID_set_and_stable_provenance: supplied IDs equal the complete current blocking set and review/manifests/risks remain stable through publication
  nonempty_feedback_and_owner_evidence_current: feedback is nonempty and upstream finding, owner artifact/approval, origin and observation provenance equal the current snapshot
  all_changed_paths_are_selected_owner_artifact_and_hashes_match_evidence_and_no_other_unreconciled_condition: every current changed approved path is the selected owner artifact, hashes equal pending evidence, and a fresh classifier observes no additional condition
  required_inputs_readable_old_new_manifests_distinct_and_new_manifest_stable_through_publication: every required path is readable, old/new manifest hashes differ, and the new manifest is unchanged through target publication
  forbidden_paths_equal_saved_hashes_and_allowed_provenance_unchanged: every forbidden path equals its pre-run hash, pending attempt is the origin, and all allowed input hashes equal pending evidence
  approval_guard: current artifact, required review, author/reviewer manifests, active risks and approvals are current and there is no unaccepted blocking finding
  git_isolation_guard: snapshot has no unmerged/unsupported index feature or staged package overlap and an exact package-only approval commit plus replayed non-package staged delta can be built without modifying protected bytes
  expected_commit_reachable_candidate_index_installed_and_all_verifications_pass: expected commit is reachable with exact parent/tree/message, candidate real index is installed, package bytes and non-package staged delta match intent, and protected worktree bytes are unchanged
  head_index_package_equal_saved_stable_snapshot: HEAD/ref, real index, package and protected worktree equal the intent's saved pre-operation snapshot
  complete_before_expected_observed_evidence: conflict record contains attempt identity, phase, before/expected/observed HEAD/ref/index/package/worktree snapshots, overlaps and target transition
  current_approval_guards_and_only_router_temporary_cleanup: approval_guard is true and every removal target is explicitly Router-owned by the terminalizable intent
  current_approval_guards_and_isolatable_current_snapshot: approval_guard and git_isolation_guard are true for the current snapshot
  nonempty_feedback_and_only_router_temporary_abandonment: feedback is nonempty and abandonment/removal touches only Router-owned intent data
  nonempty_text: supplied question text is nonempty
  no_irreversible_router_git_step_in_progress: no publication operation is active and no commit intent has a moved ref or unreconciled real index
  true: always passes

launch_profiles:
  L-R004: {origin_rule: R004, role: Spec-Reviewer, stage: earliest_affected, status: reviewing, condition: always}
  L-G003: {origin_rule: G003, role: author-for-target-stage, stage: target.stage, status: target.status, condition: target.status-is-drafting}
  L-G004: {origin_rule: G004, role: author-for-target-stage, stage: target.stage, status: target.status, condition: target.status-is-drafting}
  L-A002: {origin_rule: A002, role: saved-current-role, stage: current.stage, status: current.status, condition: always}
  L401: {origin_rule: T401, role: Framer, stage: idea, status: drafting, condition: always}
  L403: {origin_rule: T403, role: Spec-Reviewer, stage: current.stage, status: reviewing, condition: always}
  L405: {origin_rule: T405, role: author-for-current-stage, stage: current.stage, status: drafting, condition: always}
  L407: {origin_rule: T407, role: author-for-current-stage, stage: current.stage, status: revising, condition: always}
  L412: {origin_rule: T412, role: author-for-current-stage, stage: current.stage, status: idea-drafting-else-revising, condition: always}
  L413: {origin_rule: T413, role: author-for-current-stage, stage: current.stage, status: revising, condition: always}
  L415: {origin_rule: T415, role: author-for-owner-stage, stage: owner_stage, status: idea-drafting-else-revising, condition: always}
  L416: {origin_rule: T416, role: author-for-owner-stage, stage: owner_stage, status: idea-drafting-else-revising, condition: always}
  L417: {origin_rule: T417, role: author-for-earliest-affected-stage, stage: earliest_affected, status: idea-drafting-else-revising, condition: always}
  L418: {origin_rule: T418, role: pending.origin.role, stage: saved_stage, status: saved_origin_status, condition: always}
  L419: {origin_rule: T419, role: author-for-next-stage, stage: next_stage, status: drafting, condition: always}
  L422: {origin_rule: T422, role: author-for-next-stage, stage: next_stage, status: drafting, condition: source_stage-is-not-plan}
  L427: {origin_rule: T427, role: author-for-current-stage, stage: current.stage, status: idea-drafting-else-revising, condition: always}

attempt_execution:
  entry: scheduled-launch phase evaluates these rows only for the durable scheduled_launch produced by a named launch profile after publication recovery, content reconciliation and Git recovery have completed; a false launch-profile condition clears scheduled_launch with no attempt or call
  input_resolution: only the selected profile role's TOML declaration is resolved; its bytes may enter only that role's invocation workspace
  rules:
    - id: E001
      priority: 1
      when: {launch_profile: "*", required_project_inputs: missing-or-unreadable}
      action: {canonical_writes: [append_launch_failure_with_profile_role_stage_declaration_manifest_and_missing_paths, clear_scheduled_launch], counter: keep, active_attempt: null, pending: keep, commit_intent: keep, launch: none, retry: explicit-resume-only, diagnostic: missing-required-project-input, event_behavior: consume, phase_behavior: consume}
    - id: E002
      priority: 2
      when: {launch_profile: "*", required_project_inputs: readable, preparation: failed}
      action: {canonical_writes: [append_launch_failure_with_profile_role_stage_invocation_inputs_and_diagnostic, clear_scheduled_launch], counter: keep, active_attempt: null, pending: keep, commit_intent: keep, launch: none, retry: explicit-resume-only, diagnostic: attempt-preparation-failure, event_behavior: consume, phase_behavior: consume}
    - id: E003
      priority: 3
      when: {launch_profile: "*", required_project_inputs: readable, preparation: succeeded, invocation: failed-before-valid-outbox}
      action: {canonical_writes: [clear_scheduled_launch, publish_prepared_then_launched_attempt_if_not_already_durable, mark_attempt_failed_and_abandoned_with_invocation_diagnostic, clear_active_attempt], counter: keep, active_attempt: null, pending: keep, commit_intent: keep, adapter_calls: one, retry: explicit-resume-only, diagnostic: invocation-failure, event_behavior: consume, phase_behavior: consume}
    - id: E004
      priority: 4
      when: {launch_profile: "*", required_project_inputs: readable, preparation: succeeded, invocation: returned}
      action: {canonical_writes: [clear_scheduled_launch, publish_prepared_then_launched_attempt_before_call], counter: keep, active_attempt: launched, pending: keep, commit_intent: keep, adapter_calls: one, emit: inspect-current-outbox, retry: none, host: restart-at-publication-recovery-with-fresh-snapshot, event_behavior: replace-with-dispatch, phase_behavior: consume}

attempt_failure_invariants:
  - E001 and E002 do not increment attempt_seq because no identity was published; E003 and E004 increment it exactly once and never reuse it
  - each failure preserves the already-published target and counter of its launch profile, creates no pending, performs no Git write, and preserves the profile's already-published intent disposition
  - L-G003, L-G004 and L422 see commit_intent completed before attempt execution; their failures therefore remain at the approved target with no live intent
  - L-R004 retains review invalidation evidence; L-A002 retains abandonment of the prior attempt; all other profiles retain exactly their origin rule's non-preparation canonical effects
  - only a later explicit resume can schedule a replacement after E001, E002 or E003; no failure row recursively launches

publication_recovery:
  - id: P000
    priority: 9
    when: {operation: none}
    action: {event_behavior: keep, phase_behavior: continue-next}
  - id: P001
    priority: 10
    when: {operation: publication, journal_shape: target-complete}
    action: {canonical_writes: [publish_exact_target_state, clear_operation], counter: target, active_attempt: target, event_behavior: keep, phase_behavior: continue-next}
  - id: P002
    priority: 11
    when: {operation: publication, journal_shape: source-intact}
    action: {canonical_writes: [publish_exact_source_state, clear_operation], counter: source, active_attempt: source, event_behavior: keep, phase_behavior: continue-next}
  - id: P003
    priority: 12
    when: {operation: publication, journal_shape: subset-completable, all_candidates_valid: true}
    action: {canonical_writes: [finish_payloads_from_journal, publish_exact_target_state, clear_operation], counter: target, active_attempt: target, event_behavior: keep, phase_behavior: continue-next}
  - id: P004
    priority: 13
    when: {operation: publication, journal_shape: subset-restorable, all_before_images_valid: true}
    action: {canonical_writes: [restore_all_before_images, publish_exact_source_state, clear_operation], counter: source, active_attempt: source, event_behavior: keep, phase_behavior: continue-next}
  - id: P005
    priority: 14
    when: {operation: publication, journal_shape: "*"}
    action: {host: integrity-diagnostic, diagnostic: publication-integrity-failure}

git_recovery:
  - id: G001
    priority: 20
    when: {commit_intent: none}
    action: {event_behavior: keep, phase_behavior: continue-next}
  - id: G002
    priority: 21
    when: {commit_intent: active, target_state: already-published, expected_commit: reachable, real_index: candidate}
    action: {canonical_writes: [complete_intent], counter: zero, active_attempt: target, commit_intent: completed, event_behavior: keep, phase_behavior: continue-next}
  - id: G003
    priority: 22
    when: {commit_intent: active, expected_commit: reachable, real_index: saved-before-image, candidate_index: valid}
    action: {canonical_writes: [journal_index_reconciled, publish_target_state, complete_intent], git_writes: [atomic_install_candidate_real_index], counter: zero, active_attempt: null, commit_intent: completed, launch: scheduled-one, launch_profile: L-G003}
  - id: G004
    priority: 23
    when: {commit_intent: active, expected_commit: reachable, real_index: candidate}
    action: {canonical_writes: [publish_target_state_if_absent, complete_intent], counter: zero, active_attempt: null, commit_intent: completed, launch: scheduled-one, launch_profile: L-G004}
  - id: G005
    priority: 24
    when: {commit_intent: active, head: saved-old, real_index: saved-before-image, package_and_guards: stable}
    action: {target: source_stage/awaiting-approval/null, canonical_writes: [finish_intent_not_created_with_diagnostic], counter: keep, active_attempt: null, commit_intent: completed, diagnostic: git-not-created, event_behavior: keep, phase_behavior: continue-next}
  - id: G006
    priority: 25
    when: {commit_intent: active, shape: "*"}
    action: {target: source_stage/awaiting-decision/commit-conflict, canonical_writes: [publish_full_conflict_evidence], counter: keep, active_attempt: null, pending: commit-conflict, commit_intent: keep, diagnostic: git-conflict, event_behavior: keep, phase_behavior: continue-next}

scheduled_launch:
  - id: S001
    priority: 26
    when: {scheduled_launch: none}
    action: {event_behavior: keep, phase_behavior: continue-next}
  - id: S002
    priority: 27
    when: {scheduled_launch: current}
    action: {host: execute-exactly-one-matching-attempt-execution-row, event_behavior: consume, phase_behavior: consume}

attempt_outbox_recovery:
  - id: A001
    priority: 30
    when: {event: [resume, inspect-current-outbox], state_role: [author, reviewer], current_outbox: complete-valid-current}
    action: {dispatch: bound_internal_result, event_behavior: replace-with-dispatch, phase_behavior: continue-to-event-dispatch}
  - id: A002
    priority: 31
    when: {event: resume, state_role: [author, reviewer], current_outbox: absent-incomplete-or-invalid}
    action: {canonical_writes: [abandon_current_attempt_if_any], counter: keep, active_attempt: null, launch: scheduled-one, launch_profile: L-A002, retry: explicit-resume-only}
  - id: A003
    priority: 32
    when: {event: resume, state_role: null}
    action: {event_behavior: keep, phase_behavior: continue-to-event-dispatch}
  - id: A004
    priority: 33
    when: {event: inspect-current-outbox, state_role: [author, reviewer], current_outbox: absent-incomplete-or-invalid, boundary_evidence: absent}
    action: {dispatch: role-failure, event_behavior: replace-with-dispatch, phase_behavior: continue-to-event-dispatch}
  - id: A005
    priority: 34
    when: {event: inspect-current-outbox, state_role: [author, reviewer], boundary_evidence: complete}
    action: {dispatch: boundary-violation, event_behavior: replace-with-dispatch, phase_behavior: continue-to-event-dispatch}
  - id: A099
    priority: 99
    when: {event: "*"}
    action: {event_behavior: keep, phase_behavior: continue-to-event-dispatch}

content_reconciliation:
  - id: R001
    priority: 40
    when: {status: aborted}
    action: {event_behavior: keep, phase_behavior: continue-next}
  - id: R002
    priority: 41
    when: {approved_artifact_mutations: nonempty}
    action: {target: earliest_changed_owner/awaiting-decision/manual-mutation, canonical_writes: [cancel_scheduled_launch_if_any, fence_and_abandon_active_attempt_if_any, invalidate_owner_and_downstream, publish_complete_classifier_evidence], counter: keep, active_attempt: null, pending: manual-mutation, diagnostic: reconciliation-manual-mutation}
  - id: R003
    priority: 42
    when: {approved_artifact_mutations: empty, changed_role_manifests_or_declarations: nonempty}
    action: {target: earliest_affected/awaiting-decision/stale-input, canonical_writes: [cancel_scheduled_launch_if_any, fence_and_abandon_active_attempt_if_any, invalidate_dependent_reviews_risks_approvals, publish_distinct_old_new_manifests_and_complete_evidence], counter: keep, active_attempt: null, pending: stale-input, diagnostic: reconciliation-stale-input}
  - id: R004
    priority: 43
    when: {higher_classes: empty, review_provenance_mismatches: nonempty}
    action: {target: earliest_affected/reviewing/null, canonical_writes: [cancel_scheduled_launch_if_any, fence_and_abandon_active_attempt_if_any, deactivate_review_and_dependents, retain_invalidation_evidence], counter: keep, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L-R004, diagnostic: reconciliation-review-invalidation}
  - id: R005
    priority: 44
    when: {higher_classes: empty, review_provenance_mismatches: empty, invalid_risks_expose_blocking: true}
    action: {target: earliest_affected/awaiting-decision/unresolved-findings, canonical_writes: [cancel_scheduled_launch_if_any, fence_and_abandon_active_attempt_if_any, deactivate_risks_and_approvals, publish_risk_finding_and_snapshot_evidence], counter: keep, active_attempt: null, pending: unresolved-findings, diagnostic: reconciliation-risk-invalidation}
  - id: R006
    priority: 45
    when: {higher_classes: empty, review_provenance_mismatches: empty, invalid_risks_expose_blocking: false, invalid_risks: nonempty}
    action: {target: earliest_affected/awaiting-approval/null, canonical_writes: [cancel_scheduled_launch_if_any, fence_and_abandon_active_attempt_if_any, deactivate_risks_and_approvals, retain_invalidation_evidence], counter: keep, active_attempt: null, pending: null, diagnostic: reconciliation-risk-invalidation}
  - id: R007
    priority: 46
    when: {all_reconciliation_classes: empty}
    action: {event_behavior: keep, phase_behavior: continue-next}

event_rules:
  - id: T400
    priority: 100
    match: {stage: "*", status: "*", pending: "*", event: [author-valid, blocking-question, upstream-finding, review-pass, review-changes-required, role-failure, boundary-violation], attempt_binding: stale-or-noncurrent}
    success: {target: same, canonical_writes: [], git_writes: [], counter: keep, active_attempt: keep, launch: none, host: stale-result-diagnostic}
  - id: T401
    priority: 101
    match: {flow: absent, event: start-new}
    guard: unique_or_explicitly_chosen_helper_id_and_valid_source
    success: {target: idea/drafting/null, canonical_writes: [source_payload_and_hash, empty_approvals, counter_zero], counter: zero, active_attempt: null, launch: scheduled-one, launch_profile: L401}
    failure: {ref: guard_failure}
  - id: T402
    priority: 102
    match: {stage: idea, status: drafting, pending: null, event: author-valid, attempt_binding: current}
    guard: valid_Framer_candidate_and_unchanged_inputs
    success: {target: idea/awaiting-approval/null, canonical_writes: [publish_idea, provenance, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T403
    priority: 103
    match: {stage: [requirements, design, plan], status: [drafting, revising], pending: null, event: author-valid, attempt_binding: current}
    guard: valid_owned_candidate_boundary_and_unchanged_inputs
    success: {target: same_stage/reviewing/null, canonical_writes: [publish_owned_artifact, provenance, clear_author_attempt], counter: keep, active_attempt: null, launch: scheduled-one, launch_profile: L403}
    failure: {ref: guard_failure}
  - id: T404
    priority: 104
    match: {stage: "*", status: [drafting, revising], pending: null, event: blocking-question, attempt_binding: current}
    guard: exactly_one_nonempty_question_and_legal_author_status
    success: {target: same_stage/awaiting-decision/clarification, canonical_writes: [question_hash, originating_stage_status_counter, complete_revision_context, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T405
    priority: 105
    match: {stage: "*", status: awaiting-decision, pending: clarification, event: answer-clarification}
    guard: nonempty_answer_and_unchanged_pending_inputs
    success: {target: same_stage/drafting/null, canonical_writes: [exact_answer_and_hash, retain_question_origin_status_counter_and_revision_context, clear_pending], counter: keep, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L405}
    failure: {ref: guard_failure}
  - id: T406
    priority: 106
    match: {stage: [requirements, design, plan], status: reviewing, pending: null, event: review-pass, attempt_binding: current}
    guard: valid_current_review_with_no_blocking_findings
    success: {target: same_stage/awaiting-approval/null, canonical_writes: [publish_review, provenance, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T407
    priority: 107
    match: {stage: [requirements, design, plan], status: reviewing, pending: null, event: review-changes-required, counter: 0..2, attempt_binding: current}
    guard: valid_current_review_with_blocking_findings
    success: {target: same_stage/revising/null, canonical_writes: [publish_review, revision_findings, clear_reviewer_attempt], counter: increment-one, active_attempt: null, launch: scheduled-one, launch_profile: L407}
    failure: {ref: guard_failure}
  - id: T408
    priority: 108
    match: {stage: [requirements, design, plan], status: reviewing, pending: null, event: review-changes-required, counter: 3, attempt_binding: current}
    guard: valid_current_review_with_blocking_findings
    success: {target: same_stage/awaiting-decision/unresolved-findings, canonical_writes: [publish_review, current_blocking_IDs_and_provenance, originating_counter, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T409
    priority: 109
    match: {stage: "*", status: [drafting, reviewing, revising], pending: null, event: role-failure, attempt_binding: current}
    guard: adapter_failure_before_valid_publication
    success: {target: same, canonical_writes: [record_failed_attempt_diagnostic, clear_active_attempt], counter: keep, active_attempt: null, retry: explicit-resume-only}
    failure: {ref: guard_failure}
  - id: T410
    priority: 110
    match: {stage: "*", status: [drafting, reviewing, revising], pending: null, event: boundary-violation, attempt_binding: current}
    guard: complete_boundary_evidence
    success: {target: same_stage/awaiting-decision/boundary-violation, canonical_writes: [publish_attempt_paths_hashes_provenance_and_origin_status, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T411
    priority: 111
    match: {stage: [requirements, design, plan], status: [drafting, reviewing, revising], pending: null, event: upstream-finding, attempt_binding: current, origin_role_status: [current-author@drafting, current-author@revising, Spec-Reviewer@reviewing]}
    guard: valid_upstream_finding
    success: {target: owner_stage/awaiting-decision/upstream-revision, canonical_writes: [publish_finding_provenance_origin_checkpoint_and_counter, clear_active_attempt], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T412
    priority: 112
    match: {stage: "*", status: awaiting-approval, pending: null, event: revise}
    guard: nonempty_feedback_and_current_checkpoint
    success: {target: idea_drafting_else_same_stage_revising, canonical_writes: [feedback, invalidate_current_and_downstream], counter: zero, active_attempt: null, launch: scheduled-one, launch_profile: L412}
    failure: {ref: guard_failure}
  - id: T413
    priority: 113
    match: {stage: [requirements, design, plan], status: awaiting-decision, pending: unresolved-findings, event: revise}
    guard: nonempty_feedback_and_current_pending
    success: {target: same_stage/revising/null, canonical_writes: [feedback, clear_pending, invalidate_current_and_downstream], counter: zero, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L413}
    failure: {ref: guard_failure}
  - id: T414
    priority: 114
    match: {stage: [requirements, design, plan], status: awaiting-decision, pending: unresolved-findings, event: accept-risk}
    guard: exact_complete_current_blocking_ID_set_and_stable_provenance
    success: {target: same_stage/awaiting-approval/null, canonical_writes: [append_all_risk_records_atomically, clear_pending], counter: keep, launch: none}
    failure: {ref: guard_failure}
  - id: T415
    priority: 115
    match: {stage: "*", status: awaiting-decision, pending: upstream-revision, event: revise}
    guard: nonempty_feedback_and_owner_evidence_current
    success: {target: owner_idea_drafting_else_owner_revising, canonical_writes: [feedback, clear_pending, invalidate_owner_and_downstream, retain_stale_files], counter: zero, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L415}
    failure: {ref: guard_failure}
  - id: T416
    priority: 116
    match: {stage: "*", status: awaiting-decision, pending: manual-mutation, event: revise}
    guard: all_changed_paths_are_selected_owner_artifact_and_hashes_match_evidence_and_no_other_unreconciled_condition
    success: {target: owner_idea_drafting_else_owner_revising, canonical_writes: [accept_current_bytes_as_revision_input, feedback, clear_pending, invalidate_owner_and_downstream], counter: zero, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L416}
    failure: {ref: guard_failure}
  - id: T417
    priority: 117
    match: {stage: "*", status: awaiting-decision, pending: stale-input, event: revise}
    guard: required_inputs_readable_old_new_manifests_distinct_and_new_manifest_stable_through_publication
    success: {target: earliest_affected_idea_drafting_else_revising, canonical_writes: [new_manifest, feedback, clear_pending, retain_invalidations], counter: zero, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L417}
    failure: {ref: guard_failure}
  - id: T418
    priority: 118
    match: {stage: "*", status: awaiting-decision, pending: boundary-violation, event: retry-role}
    guard: forbidden_paths_equal_saved_hashes_and_allowed_provenance_unchanged
    success: {target: saved_stage/saved_origin_status/null, canonical_writes: [clear_pending], counter: keep, active_attempt: null, pending: null, launch: scheduled-one, launch_profile: L418}
    failure: {ref: guard_failure}
  - id: T419
    priority: 119
    match: {stage: [idea, requirements, design], status: awaiting-approval, pending: null, event: continue}
    guard: approval_guard
    success: {target: next_stage/drafting/null, canonical_writes: [publish_approval], counter: zero, active_attempt: null, launch: scheduled-one, launch_profile: L419}
    failure: {ref: guard_failure}
  - id: T420
    priority: 120
    match: {stage: plan, status: awaiting-approval, pending: null, event: continue}
    guard: approval_guard
    success: {target: plan/approved/null, canonical_writes: [publish_approval], counter: zero, launch: none}
    failure: {ref: guard_failure}
  - id: T421A
    priority: 121
    match: {stage: "*", status: awaiting-approval, pending: null, event: continue-and-commit, approval_guard: fail}
    guard: true
    success: {diagnostic: approval-guard-failed}
  - id: T421B
    priority: 121.1
    match: {stage: "*", status: awaiting-approval, pending: null, event: continue-and-commit, approval_guard: pass, git_isolation_guard: fail}
    guard: true
    success: {target: same_stage/awaiting-decision/commit-conflict, canonical_writes: [publish_isolation_rejected_intent_with_full_snapshot_and_conflict_pending], counter: keep, active_attempt: null, pending: commit-conflict, commit_intent: isolation-rejected, diagnostic: git-isolation-rejected}
  - id: T421C
    priority: 121.2
    match: {stage: "*", status: awaiting-approval, pending: null, event: continue-and-commit, approval_guard: pass, git_isolation_guard: pass}
    guard: true
    success: {target: same, canonical_writes: [publish_prepared_commit_intent], git_writes: [one_journaled_git_attempt], counter: keep, active_attempt: null, commit_intent: prepared, emit: exactly-one-git-outcome}
  - id: T422
    priority: 122
    match: {stage: "*", status: awaiting-approval, pending: null, event: git-verified, commit_intent: current}
    guard: expected_commit_reachable_candidate_index_installed_and_all_verifications_pass
    success: {target: plan_approved_else_next_stage_drafting, canonical_writes: [publish_approval_target, complete_intent], counter: zero, active_attempt: null, commit_intent: completed, launch: scheduled-one, launch_profile: L422}
    failure: {ref: guard_failure}
  - id: T423
    priority: 123
    match: {stage: "*", status: awaiting-approval, pending: null, event: [git-not-created, git-ordinary-failure], commit_intent: current}
    guard: head_index_package_equal_saved_stable_snapshot
    success: {target: same_stage/awaiting-approval/null, canonical_writes: [complete_intent_with_outcome_and_diagnostic], counter: keep, active_attempt: null, commit_intent: completed, retry: explicit-event-only}
    failure: {ref: guard_failure}
  - id: T424
    priority: 124
    match: {stage: "*", status: awaiting-approval, pending: null, event: git-conflict, commit_intent: current}
    guard: complete_before_expected_observed_evidence
    success: {target: same_stage/awaiting-decision/commit-conflict, canonical_writes: [publish_post_intent_conflict_pending_and_retain_intent], counter: keep, active_attempt: null, pending: commit-conflict, commit_intent: keep, diagnostic: git-post-intent-conflict}
    failure: {ref: guard_failure}
  - id: T425
    priority: 125
    match: {stage: "*", status: awaiting-decision, pending: commit-conflict, event: cancel-commit}
    guard: current_approval_guards_and_only_router_temporary_cleanup
    success: {target: same_stage/awaiting-approval/null, canonical_writes: [mark_intent_cancelled, retain_conflict_evidence, clear_pending, remove_named_router_temps], counter: keep, active_attempt: null, pending: null, commit_intent: cancelled}
    failure: {ref: guard_failure}
  - id: T426
    priority: 126
    match: {stage: "*", status: awaiting-decision, pending: commit-conflict, event: retry-commit}
    guard: current_approval_guards_and_isolatable_current_snapshot
    success: {target: same, canonical_writes: [finish_old_intent, publish_new_prepared_intent], git_writes: [one_journaled_git_attempt_or_exact_prior_commit_reconciliation], counter: keep, active_attempt: null, commit_intent: prepared, emit: exactly-one-git-outcome}
    failure: {ref: guard_failure}
  - id: T427
    priority: 127
    match: {stage: "*", status: awaiting-decision, pending: commit-conflict, event: revise}
    guard: nonempty_feedback_and_only_router_temporary_abandonment
    success: {target: idea_drafting_else_same_stage_revising, canonical_writes: [mark_intent_abandoned, retain_conflict_evidence, feedback, clear_pending, remove_named_router_temps], counter: zero, active_attempt: null, pending: null, commit_intent: abandoned, launch: scheduled-one, launch_profile: L427}
    failure: {ref: guard_failure}
  - id: T428
    priority: 128
    match: {stage: "*", status: [awaiting-approval, awaiting-decision, approved, aborted], pending: "*", event: question}
    guard: nonempty_text
    success: {target: same, canonical_writes: [], git_writes: [], counter: keep, active_attempt: keep, launch: none, host: answer_without_state_change}
    failure: {ref: guard_failure}
  - id: T429
    priority: 129
    match: {stage: "*", status: [awaiting-approval, awaiting-decision, approved, aborted], pending: "*", event: stop}
    guard: true
    success: {target: same, canonical_writes: [], git_writes: [], counter: keep, active_attempt: keep, launch: none, host: end_session}
  - id: T430
    priority: 130
    match: {stage: "*", status: [drafting, reviewing, revising, awaiting-approval, awaiting-decision], pending: "*", event: abort}
    guard: no_irreversible_router_git_step_in_progress
    success: {target: same_stage/aborted/null, canonical_writes: [mark_active_attempt_abandoned_if_any, mark_intent_abandoned_if_safe, clear_pending_preserving_evidence], git_writes: [], counter: keep, launch: none, host: terminal}
    failure: {ref: guard_failure}
  - id: T431
    priority: 131
    match: {stage: "*", status: [awaiting-approval, awaiting-decision, approved, aborted], pending: "*", event: resume}
    guard: true
    success: {target: same, canonical_writes: [], git_writes: [], counter: keep, active_attempt: keep, launch: none, host: display_checkpoint}
  - id: T998
    priority: 998
    match: {flow: absent, event: "*"}
    success: {ref: invalid_event}
  - id: T999
    priority: 999
    match: {stage: "*", status: "*", pending: "*", event: "*", guard_outcome: "*"}
    success: {ref: invalid_event}
```

## Open questions

- **Second real adapter target:** Which non-Codex agent system will be used for the second portability proof? Impact: only the scope and implementation schedule of the later adapter; it does not change the protocol or first implementation. Resolve before claiming cross-provider portability under REQ-105.
- **Future history compaction:** Should a later schema move inactive manifests, risks, and ID tombstones out of `state.yaml` into separate content-addressed canonical files? Impact: package size and review ergonomics only; schema v1 remains coherent and complete. Resolve before designing a schema-version migration, not before implementation of v1.
