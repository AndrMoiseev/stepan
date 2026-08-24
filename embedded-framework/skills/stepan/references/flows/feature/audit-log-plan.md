# Feature audit log implementation plan

Status: non-normative implementation plan. Execute against
[`audit-log-spec.md`](audit-log-spec.md). Do not treat this plan as a runtime
workflow resource.

## Delivery strategy

The audit log was implemented as one breaking feature-flow change. The audit
specification remained non-normative until script support, runtime contracts,
role contracts, and tests were complete; activation occurs only in the final
step.

Preserve these boundaries throughout the work:

- change only the Stepan skill;
- keep `mem-log.md` out of every role's inputs and write boundary;
- keep `request.md`, state, clarifications, reviews, and approved artifacts as
  primary workflow data;
- add no third-party Python dependency;
- add no public audit command;
- add no legacy-state or legacy-receipt compatibility path; and
- do not archive prior artifact or review bodies.

## Planned file impact

| Path | Planned change |
| --- | --- |
| `scripts/stepan.py` | Add audit schemas, rendering, parsing, hashes, outbox flush and recovery, receipt decision validation, decision diffing, state/log cross-validation, commit-trailer support, and self-tests. |
| `references/flows/feature/audit-log-spec.md` | Resolve any implementation-proven schema details, change status to normative at activation, and retain the complete audit contract. |
| `references/flows/feature/protocol.md` | Load the audit contract and integrate storage, routing, checkpoints, lifecycle, failure, and user-interaction rules. |
| `references/flows/feature/execution.md` | Extend receipts and manifests, add revision inputs and executor provenance, and enforce audit gates around durable runs. |
| `references/flows/feature/router.md` | Assign audit ownership to the logical router and keep the thin launcher outside the audit/data boundary. |
| `references/flows/feature/adapters/codex.md` | Define available requested/effective executor provenance and receipt behavior for Codex runs. |
| `references/flows/feature/adapters/claude-code.md` | Define available requested/effective executor provenance and receipt behavior for Claude Code runs. |
| `references/flows/feature/roles/*.md` | Require the role-specific full material-decision snapshot in successful receipts. |
| `references/modules/audit/decisions.md` | New reusable semantic rules for material decisions, alternatives, user-authority normalizations, and full snapshots. |
| Existing artifact/review modules | Change only where stable reference or decision-coverage rules cannot remain role-local. Avoid duplicating the common receipt contract. |

No change is expected in `SKILL.md`, `agents/openai.yaml`, or the `init`
workflow: the existing router already loads the selected feature protocol, and
project initialization does not create individual feature specifications.

## Phase 1 — Freeze executable schemas

Status: completed

### 1.1 Define audit constants and identities

In `scripts/stepan.py`:

- add allowed audit kinds, event names, actors, stages, and role-specific
  decision kinds;
- define event ID, transaction ID, event-key, and semantic-hash rules;
- use `MEM-` IDs with at least six decimal digits and no sequence reuse;
- define the canonical `recorded_at` UTC timestamp format;
- define the audit-log size bound; start with 10 MiB unless focused tests show a
  smaller safe bound; and
- keep audit limits separate from ordinary control-file and artifact limits.

### 1.2 Freeze the state audit object

Turn the specification's conceptual `state.yaml.audit` object into one exact,
strict schema:

- immutable project-relative `log_path`;
- full-file byte length and SHA-256;
- nullable head ID and head event hash;
- positive next event sequence;
- role-namespaced decision index;
- nullable single ordered outbox transaction; and
- explicit audit schema version 1.

Reject unknown fields, aliases, non-canonical paths, invalid hashes, duplicate
decision keys, and an outbox prepared against a different head.

### 1.3 Freeze queued and durable event schemas

Define exact required and optional fields for each event family:

- `interaction`: initial request, blocking question, verbatim response,
  normalized user decision, user question, agent answer, approval, revision
  feedback, risk acceptance, collision/recovery choice, and stop;
- `decision`: introduced, revised, and retired agent decisions; and
- `lifecycle`: specification creation, stage entry, run reservation/result,
  artifact/review acceptance, automatic revision, wait/interruption/failure,
  upstream invalidation, commit linkage, resume, and completion.

Use stable event keys derived only from durable workflow identity. Specify the
canonical event order for every multi-event transaction.

### 1.4 Freeze completed receipt decisions

Extend the existing completed receipt schema with required `decisions` and
strictly define:

- `key`;
- `authority: agent | user`;
- role-appropriate `kind`;
- non-empty `summary` and `rationale`;
- ordered material alternatives with `option` and `rejected_because`;
- ordered artifact references; and
- ordered source event keys for user-authority normalization.

Keep blocked and failed receipt shapes unchanged. Preserve mailbox executor
metadata.

Exit condition: one set of Python constants and validation functions represents
the exact schemas; later phases do not recreate them in separate code paths.

## Phase 2 — Implement canonical Markdown and integrity

Status: completed

### 2.1 Header renderer and parser

Implement pure functions to create and parse the immutable header containing:

- log schema version;
- specification identity;
- canonical `request.md` path; and
- immutable request hash.

Require canonical UTF-8 and trailing-newline behavior.

### 2.2 Event renderer and parser

Implement one canonical renderer per event schema and a strict inverse parser.
Keep metadata labels in English and preserve user/role text exactly. Safely
represent arbitrary multiline content, including nested Markdown fences,
without permitting it to terminate or forge an event block.

Do not maintain an independent hidden JSON representation inside the Markdown
file. The validated Markdown is the durable representation.

### 2.3 Hash chain

Implement and document one non-self-referential event-hash rule. Validate:

- consecutive event IDs;
- unique event keys;
- each previous-event hash;
- each event hash;
- full log hash and byte length; and
- the state head and next sequence.

### 2.4 Pure-function tests

Add self-tests for:

- empty header round trip;
- every event type round trip;
- Unicode and multiline verbatim text;
- embedded backticks and Markdown fences;
- changed metadata or body text;
- deleted, reordered, duplicated, or truncated events;
- invalid previous hashes;
- extra manual append; and
- size-limit rejection before parsing.

Exit condition: canonical log bytes round-trip exactly and every tamper fixture
fails deterministically.

## Phase 3 — Add state/log initialization and validation

Status: completed

### 3.1 Require audit state

Update `validate_state_value` to require the new audit object while retaining
feature state `schema_version: 1`. Old states without `audit` must fail with no
migration fallback.

Keep pure state-structure validation separate from filesystem cross-validation
so initialization and recovery can validate the correct intermediate state.

### 3.2 Cross-validate the specification log

Update `validate_state_path` to resolve the exact specification-local
`mem-log.md` and validate:

- header identity and request hash;
- first-event request equivalence with `request.md`;
- state byte length, file hash, head, sequence, and decision index; and
- outbox consistency with the accepted head.

Do not read the log as product context or pass it to a role.

### 3.3 Initialize one specification atomically

Add deterministic initialization support that creates:

- canonical immutable `request.md`;
- header plus first `initial-request-captured` event in `mem-log.md`; and
- initial `state.yaml.audit` metadata with a null outbox.

Retain the existing rollback rule: on incomplete initialization remove only
files created by that attempt. Validate all three files before the first role
reservation.

### 3.4 Initialization tests

Test Unicode requests, canonical trailing newlines, collisions, partial-write
rollback, wrong request hash, wrong log path, old-state rejection, and attempts
to initialize over existing files.

Exit condition: no valid new feature state can exist without a matching audit
log and verbatim initial-request event.

## Phase 4 — Implement transactional outbox and recovery

Status: completed

### 4.1 Queue invariants

Require an empty outbox before any new transition, reservation, approval,
commit, or completion. Each state mutation must queue the complete audit batch
in the same atomic state replacement.

Centralize event construction in deterministic helpers. Do not make the router
reproduce event-key, canonical payload, hash, or ordering algorithms in prose or
shell snippets.

### 4.2 Atomic flush

Implement the specified flush algorithm:

1. validate state, log, head, and queued transaction;
2. assign IDs and UTC recording times;
3. resolve intra-batch event-key relationships;
4. render all event blocks;
5. construct the new file from the exact old byte prefix plus the batch;
6. fsync and atomically replace the log;
7. update audit metadata and decision index and clear outbox atomically; and
8. reread both files and validate the result.

Use same-directory temporary files. Never rewrite accepted prior event bytes as
a repair operation.

### 4.3 Idempotent crash recovery

Handle these explicit crash boundaries:

- state queued, log unchanged;
- temporary log written, replacement not completed;
- log replaced, state still contains outbox;
- state updated, caller lost the result; and
- retry after full success.

An identical already-appended event key and payload completes metadata recovery
without duplication. A conflicting duplicate blocks.

### 4.4 Reserve-run integration

Modify `reserve-run` so one atomic state replacement both reserves the role run
and queues its run-reservation lifecycle event. Require a successful flush
before native dispatch or mailbox publication.

### 4.5 Outbox tests

Test every crash boundary, stale expected head, conflicting key, reordered
batch, repeated flush, concurrent file change, and dispatch attempted before
flush.

Exit condition: no workflow operation can progress past an unrecorded durable
state change.

## Phase 5 — Validate role decisions and compute diffs

Status: completed

### 5.1 Receipt validation

Extend `validate_receipt` so every completed receipt requires a bounded full
decision snapshot. Validate exact fields, strings, alternatives, unique keys,
authority, references, and source event keys before treating the role result as
accepted.

Keep the existing one same-agent format-repair boundary. A semantically invalid
decision snapshot is not silently reconstructed by the router.

### 5.2 Role-specific coverage

After artifact/review structural validation, enforce:

- idea-author user-authority framing links only to declared user inputs;
- requirements-author decision keys and references use canonical delta
  references present in `requirements.md`;
- requirement review includes one verdict record and every finding ID;
- design-author includes every `DES-*` exactly once;
- specification review includes one verdict record and every finding ID; and
- planner material decisions reference existing `STEP-*` entries.

An empty agent-decision list is valid only when these role-specific rules allow
it.

### 5.3 Semantic hashing and decision diff

Canonicalize and hash only semantic decision fields. Compare the full accepted
snapshot with the role's decision-index namespace and prepare deterministic
introduced, revised, and retired events. Link revised and retired events to the
previous event ID. Do not emit duplicates for unchanged decisions.

User-authority normalization creates linked interaction events but never enters
the agent decision index.

### 5.4 Receipt and diff tests

Cover missing decisions, duplicate keys, invalid authority, empty rationale,
invalid references, alternatives without rejection reasons, missing `DES-*`,
review finding mismatch, unchanged decisions, revised decisions, retired
decisions, and user-authority normalization.

Exit condition: a role result cannot clear `active_run` until its decision
snapshot is valid and its complete audit-result batch is queued.

## Phase 6 — Correct revision manifests

Status: completed

### 6.1 Add the current artifact on revision

Update role-manifest construction and validation so `purpose: revise` includes
the current role-owned artifact as a hashed input:

- `idea.md` for idea revision;
- `requirements.md` for requirements revision;
- `design.md` for design revision; and
- `plan.md` for plan revision.

The same path remains the sole allowed output. The executor reads the current
bytes before atomically replacing them.

### 6.2 Preserve review feedback rules

Keep previous review path/hash and unresolved finding IDs in feedback where the
existing contract requires them. Do not substitute the audit log for the old
artifact or review input.

### 6.3 Manifest tests

Test draft versus revise input order, current-output hash validation, missing or
changed current artifact, mailbox/native parity, and continued rejection of
`mem-log.md` as an input or allowed write.

Exit condition: every revision role can compare its current artifact before
replacement without receiving unrelated audit history.

## Phase 7 — Add role-facing decision guidance

Status: completed

### 7.1 Create the shared audit module

Add `references/modules/audit/decisions.md` with only reusable role knowledge:

- material-decision threshold;
- result-level summary and rationale rules;
- material rejected alternatives;
- full-snapshot rather than delta semantics;
- agent versus user authority;
- source/reference requirements; and
- prohibition on private reasoning traces and audit-log access.

Do not define workflow transitions, commands, state writes, or expanded file
permissions in the module.

### 7.2 Link the module directly from role briefs

Add the module as a direct `Resources` link in every feature role brief. Keep
role-specific coverage in each brief:

- idea framing normalization;
- requirements delta decisions;
- requirement-review verdict and findings;
- complete `DES-*` coverage;
- specification-review verdict and findings; and
- material plan sequencing/dependency/verification decisions.

Update role-resource manifest self-tests for the new ordered resource list.

### 7.3 Preserve response and write boundaries

Require decisions only in the final completed receipt. Keep the artifact or
review as the one filesystem write. Blocked and failed receipts remain minimal.

Exit condition: every selected role receives the minimum common decision rules
plus its own coverage rules, with no access to unrelated modules or the log.

## Phase 8 — Integrate runtime workflow contracts

Status: completed

### 8.1 Make the audit specification normative

Update the status of `audit-log-spec.md` and link it from `protocol.md` with an
instruction to read it completely before creating or changing feature state.
Keep detailed audit schemas in that file instead of duplicating them across
the protocol, execution contract, and router contract.

### 8.2 Update the feature protocol

Integrate the audit boundary into:

- invariants and tooling;
- storage and state examples;
- initialization;
- role reservation and dispatch;
- result acceptance and state transitions;
- questions, answers, approvals, revisions, accepted risks, stop, status, and
  completion;
- upstream invalidation and recovery;
- failure handling; and
- the rule that audit mechanics remain private in user-facing communication.

Explicitly make `question` product-state read-only but audit-recording. Keep
`status` unlogged, while allowing it to finish a valid interrupted outbox flush
before reporting.

### 8.3 Update execution and router contracts

In `execution.md`, document the completed receipt decision schema, revision
input, audit gates around active runs, receipt hash evidence, and executor
provenance.

In `router.md`, distinguish responsibilities:

- the thin launcher never reads or writes feature audit data;
- the dedicated logical router may queue events with state transitions; and
- only bundled deterministic operations create, parse, extend, or validate the
  Markdown log.

### 8.4 Update native adapters

Document requested and effective executor metadata separately. Record only what
each host can verify and use `unavailable` otherwise. Preserve the existing
native receipt repair and runtime-identity safety boundaries.

Exit condition: runtime documents select one audit contract and contain no
contradictory legacy receipt, state, read-only-action, or dispatch rule.

## Phase 9 — Map every workflow transition to events

Status: completed

Build one implementation table, preferably as script data used by validation,
covering each accepted transition and its ordered audit batch. Include at
least:

- new specification and initial request;
- stage owner reservation and result;
- reviewer reservation and result;
- blocked question and verbatim answer;
- normalized clarification acceptance;
- automatic and user-directed revision;
- third failed automatic revision;
- artifact approval and accepted risk;
- artifact question and answer;
- stop and resume;
- mailbox wait, completion, timeout, and malformed response;
- native interruption and recovery;
- upstream invalidation;
- checkpoint commit selection/failure; and
- final completion.

For each transition verify:

- the primary state mutation occurs with the corresponding outbox batch;
- no duplicate event is emitted on resume;
- event order matches causal order;
- the user sees no audit mechanics; and
- integrity failure stops rather than attempting an untrusted append.

Exit condition: every branch in the protocol's routing section has an explicit
audited or intentionally unlogged outcome.

## Phase 10 — Integrate checkpoint commits

Status: completed

For `continue-and-commit`:

- flush the approval/commit-selection event first;
- retain subject `stepan(<spec-id>): approve <stage>`;
- add trailer `Stepan-Audit-Event: <MEM-id>`;
- stage only `docs/changes/specs/<spec-id>/`;
- verify the committed event ID and specification contents; and
- never predict or add the commit's own SHA to `mem-log.md`.

On failure, preserve the checkpoint, protect user/hook changes, and queue a
commit-failure event only while audit integrity remains valid.

Add tests for trailer formatting, wrong/missing event ID, unrelated staged
files, hook-created changes, commit failure, and successful content
verification.

Exit condition: Git history and the log are linked bidirectionally without a
self-referential hash.

## Phase 11 — End-to-end verification

Status: completed

### 11.1 Expand bundled self-test

Add representative complete flows covering:

- a straight-through feature with approvals;
- multiple idea or requirements clarifications;
- a design containing alternatives;
- review-driven automatic revision;
- user-decision finding and answer;
- introduced, revised, retired, and unchanged decisions;
- artifact question/answer with no product-state change;
- stop/resume;
- mailbox wait/resume;
- interrupted flush at each transaction boundary;
- audit tampering;
- upstream invalidation;
- continue-and-commit trailer linkage; and
- final plan approval.

Assert observable state, file hashes, event order, append-only prefixes, role
manifests, and receipt validation. Avoid wording-only assertions except where
canonical Markdown rendering is itself the contract.

### 11.2 Run skill validation

Run, in order:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" self-test
python "<skill-creator>/scripts/quick_validate.py" "<skill-root>"
git diff --check -- "<skill-root>"
```

Use the available Python launcher for `quick_validate.py`; do not change the
feature workflow's required `uv` invocation boundary.

### 11.3 Review progressive disclosure

Verify that:

- `SKILL.md` still only routes to the selected feature protocol;
- the feature protocol directly selects the audit contract;
- roles load the shared decision module only through direct brief links;
- audit details are not duplicated across entrypoint, protocol, and roles; and
- the implementation plan itself is not loaded by the runtime workflow.

Exit condition: all deterministic and structural checks pass, all acceptance
criteria in `audit-log-spec.md` are covered, and no runtime document still
describes the legacy state or completed receipt shape.

## Phase 12 — Activate and clean up

Status: completed

Only after all prior phases pass:

1. change `audit-log-spec.md` status from implementation specification to
   normative feature audit contract;
2. confirm `protocol.md` requires it before feature state access;
3. remove temporary fixtures or implementation notes that are not used by
   self-test or runtime routing;
4. keep this plan non-normative and unlinked from runtime resources; and
5. run the complete verification sequence again from a clean process.

Completion means the feature flow cannot create, resume, transition, dispatch,
approve, commit, or complete without a valid append-only audit trail, while
roles remain isolated from that trail and product truth remains in the existing
state and artifacts.
