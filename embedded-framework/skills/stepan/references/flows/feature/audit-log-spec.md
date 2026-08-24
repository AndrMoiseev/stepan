# Feature audit log contract

Status: normative feature audit contract, schema version 1. Every feature
workflow component must follow this file for audit state, receipts, events,
Markdown, transactional writing, validation, and recovery. The feature
protocol, execution contract, router contract, adapters, and role briefs define
where those rules apply without redefining their schemas.

## Contents

- [Outcome](#outcome)
- [Scope and boundaries](#scope-and-boundaries)
- [Storage](#storage)
- [Audit invariants](#audit-invariants)
- [Primary checkpoint commit intent](#primary-checkpoint-commit-intent)
- [State audit metadata](#state-audit-metadata)
- [Event model](#event-model)
- [Markdown format](#markdown-format)
- [User interactions](#user-interactions)
- [Role decision snapshots](#role-decision-snapshots)
- [Lifecycle events](#lifecycle-events)
- [Transactional writing](#transactional-writing)
- [Validation and recovery](#validation-and-recovery)
- [Checkpoint commits](#checkpoint-commits)
- [Breaking schema change](#breaking-schema-change)
- [Acceptance criteria](#acceptance-criteria)

## Outcome

Every feature specification has one durable, chronological audit trail named
`mem-log.md`. It records:

- questions presented during the flow;
- the user's verbatim responses and canonical workflow choices;
- normalized interpretations of free-form user decisions after a role accepts
  them;
- material decisions made by author, reviewer, design, and planning roles;
- material alternatives considered and the reason each was rejected; and
- lifecycle events needed to reconstruct how the workflow progressed.

The log explains when and why decisions were made. It is not the source of
current product truth. Current truth remains in verified `state.yaml`, the
immutable `request.md`, completed clarifications, approvals, reviews, and the
current approved specification artifacts.

## Scope and boundaries

### Included

- A human-readable, machine-validated Markdown log.
- Strict chronological ordering across all stages.
- An append-only byte-prefix guarantee.
- Hash-chain and whole-file integrity checks.
- Crash-safe, idempotent recording through a transactional outbox.
- User, agent-decision, review, artifact, execution, and workflow lifecycle
  provenance.
- Requested executor configuration and effective runtime configuration when the
  host can verify it.
- Material decision changes classified as introduced, revised, or retired.

### Excluded

- Treating the log as role input or current product authority.
- Capturing private chain-of-thought, hidden deliberation, incidental wording
  choices, formatting choices, or every implementation possibility considered.
- Preserving historical copies of artifacts or reviews. The log records their
  hashes and material decisions, but old file bodies may be overwritten.
- A public `$stepan feature audit` or `/stepan feature audit` command.
- User identity beyond the literal audit actor `user`.
- Compatibility with feature state or receipts created before this change.

Roles must never read `mem-log.md`. The router must not use it to reinterpret
product intent. A revision role instead receives its current role-owned artifact
as an explicitly declared, hashed input before that artifact is overwritten.

## Storage

Add the log to every feature specification directory:

```text
docs/changes/specs/<spec-id>/
├── state.yaml
├── mem-log.md
├── request.md
├── idea.md
├── requirements.md
├── design.md
├── plan.md
└── review/
    ├── requirements.yaml
    └── design.yaml
```

Create `mem-log.md` during specification initialization, before the first role
is dispatched. Initialization must either create a mutually consistent
`request.md`, `state.yaml`, and `mem-log.md`, or roll back only the files made by
that failed initialization attempt.

The bundled initialization operation is `initialize-feature --project-root
<root> --spec-id <id>`. It accepts one exact JSON object on standard input with
`initial_request` and the normalized `execution` snapshot. The operation assigns
one UTC timestamp, writes canonical `request.md`, then `mem-log.md`, then
`state.yaml` using exclusive creation, and validates the complete three-file
set. Any failure removes only files and otherwise-empty directories created by
that attempt. An existing specification directory is a collision and is never
treated as an idempotent retry.

The initial log contains one canonical initialization batch: `MEM-000001`
`initial-request-captured`, `MEM-000002` `specification-created`, and
`MEM-000003` `stage-entered` for the initial `idea` stage. They share the
initialization timestamp, use canonical stable keys and causal links, and leave
state at head `MEM-000003` with `next_event_sequence: 4`, an empty decision
index, and a null outbox.

The immutable request is intentionally duplicated:

- `request.md` remains the narrow, hashed working input supplied to roles; and
- the first audit interaction records the same request verbatim.

Validation must require the first event's verbatim request bytes, after only the
same canonical newline handling used for `request.md`, to match `request.md` and
`initial_request_sha256` exactly.

## Audit invariants

1. Only the logical feature router, acting through deterministic bundled-script
   operations, may create or extend `mem-log.md`.
2. Roles may neither read nor write the log. It is never a role manifest input
   or allowed output.
3. Every accepted log version must contain the previous accepted file bytes as
   an exact prefix. Existing content is never edited, reordered, normalized, or
   deleted.
4. Events use one monotonically increasing global sequence, independent of
   stage and role-run sequences.
5. A new event links to the hash of the preceding event. State also pins the
   current full-file hash and byte length.
6. Every state-changing workflow operation queues its corresponding audit batch
   in the same atomic `state.yaml` replacement that changes primary workflow
   state.
7. A non-empty audit outbox blocks any further product-state transition, role
   reservation, role dispatch, approval, commit, or completion until it has
   been flushed successfully.
8. A role may be dispatched only after the lifecycle event for its durable run
   reservation has been flushed.
9. Audit recording failures pause the workflow. They never downgrade to a
   warning or allow unaudited progress.
10. Existing events are corrected only by later linked events. A correction may
    revise or retire a prior decision but must not mutate its original record.
11. A duplicate event key with identical canonical content is idempotent. The
    same key with different content is an integrity error.
12. `recorded_at` is the UTC time assigned by the deterministic writer when the
    event becomes durable. It must not be presented as an exact external action
    time when the host did not supply one.
13. When log integrity cannot be established, do not append an integrity-failure
    event to the untrusted log. Stop and report the recovery requirement.

## Primary checkpoint commit intent

Feature state has one required `checkpoint_commit` field alongside `active_run`
and `audit`. It is normally null. Selecting `continue-and-commit` atomically
sets it to this strict primary-state object while queuing `stage-approved`:

```yaml
checkpoint_commit:
  stage: requirements
  action: continue-and-commit
  commit_message: "stepan(export-data): approve requirements"
  selection_event_key: checkpoint/export-data/stage/requirements/approved
  artifact_path: docs/changes/specs/export-data/requirements.md
  artifact_sha256: sha256:...
  review_sha256: sha256:...
  accepted_risks: []
  verbatim: "Continue and commit"
```

It is valid only at an idle `awaiting-approval` checkpoint with no pending
question or active run. The stage, artifact, and review evidence must match the
checkpoint, and `selection_event_key` must resolve to the exact queued or
durable `stage-approved` event. The selection event ID is deliberately absent:
the writer assigns it only at flush. Checkpoint commit handling resolves the
durable ID by this key after flush. Commit failure leaves the object unchanged
for an explicit retry; verified commit success applies the approval, advances
or completes the workflow, and clears it atomically. The audit log never
reconstructs this product intent.

## State audit metadata

`state.yaml` contains one required `audit` object. YAML serialization details
outside the validated data model are not normative; the data model is:

```yaml
audit:
  schema_version: 1
  log_path: docs/changes/specs/export-data/mem-log.md
  log_size_bytes: 12345
  log_sha256: sha256:...
  head_event_id: MEM-000017
  head_event_sha256: sha256:...
  next_event_sequence: 18
  decision_index:
    design-author:
      DES-002:
        event_id: MEM-000017
        semantic_sha256: sha256:...
  outbox: null
```

Before the first event, `head_event_id` and `head_event_sha256` are null,
`next_event_sequence` is `1`, and `log_size_bytes` plus `log_sha256` describe the
canonical header-only file.

`decision_index` is audit metadata, not product authority. It stores only the
latest event identity and canonical semantic hash for each role-owned material
decision key. It must not store artifact bodies or replace approved artifacts.

The outbox is either null or one ordered transaction:

```yaml
outbox:
  transaction_id: audit-tx-000018
  expected_head_event_id: MEM-000017
  expected_head_event_sha256: sha256:...
  expected_log_sha256: sha256:...
  events:
    - event_key: run/export-data--design--design-author--4/completed
      kind: lifecycle
      event: role-run-completed
      stage: design
      actor: router
      run_id: export-data--design--design-author--4
      related_events: []
      related_event_keys: []
      payload: ...
  decision_index_after: ...
```

The outbox may hold a batch so role completion, artifact acceptance, material
decision changes, review outcome, and the resulting state transition can be
appended atomically and in a deterministic order. `decision_index_after` is
applied only after the corresponding events are durable.

The schema is strict: every field shown above is required (nullable fields still
must be present), and no other field is allowed. `transaction_id` is
`audit-tx-` plus the same canonical zero-padded sequence used by the
transaction's first event. Therefore it must equal `next_event_sequence` when
the transaction is queued. A decision-index role namespace is omitted when
empty; a present namespace must contain at least one entry. Indexed event IDs
must not be later than the accepted head. `decision_index_after` may additionally
refer to event IDs assigned by its own batch, but never beyond the batch's final
sequence.

The router must not expose outbox data in ordinary user-facing messages.

## Event model

Every event has these common fields:

- `event_id`: script-assigned `MEM-` followed by a zero-padded global sequence;
- `event_key`: stable idempotency key derived from durable workflow identity;
- `recorded_at`: script-assigned UTC timestamp;
- `stage`: `workflow`, `idea`, `requirements`, `design`, or `plan`;
- `kind`: `interaction`, `decision`, or `lifecycle`;
- `event`: one allowed event name for that kind;
- `actor`: `user`, `router`, or one exact feature role name;
- optional `run_id` when the event originates from a durable role run;
- `related_events`: ordered prior event IDs when the relationship is known;
- `previous_event_sha256`: null for the first event and otherwise the preceding
  event's hash; and
- `event_sha256`: the hash of the canonical event block under the Markdown hash
  rules.

Event IDs are assigned only while flushing. Queued events refer to one another
by stable `event_key`; the writer resolves those references to final event IDs.

### Executable identities and limits

- Event IDs use the canonical formatting operation `MEM-{sequence:06d}`. The
  sequence is positive; six digits is the minimum width, not a maximum.
- Transaction IDs use `audit-tx-{first_event_sequence:06d}`.
- Event keys are at most 512 characters and consist of slash-separated stable
  identity segments. Each segment begins with an ASCII letter or digit and may
  then contain ASCII letters, digits, `.`, `_`, `:`, or `-`.
- A run event key starts with `run/<run-id>/`. Other keys start with `spec/`,
  `interaction/`, `workflow/`, or `checkpoint/`.
- A decision or normalization event uses the canonical decision key unchanged
  as its final segment when that complete key already satisfies the segment
  grammar. Otherwise the final segment is `sha256-` plus the 64 lowercase hex
  digits of SHA-256 over the exact UTF-8 decision key. The original key remains
  in the payload. Any resulting duplicate or durable-key conflict blocks the
  transaction before it is queued.
- `recorded_at` is a real calendar time formatted exactly as
  `YYYY-MM-DDTHH:MM:SSZ`; fractional seconds and numeric UTC offsets are not
  canonical.
- The audit log limit is 10 MiB, separate from control-file and artifact limits.
- One outbox contains at most 256 events. One receipt contains at most 256
  decisions; one decision contains at most 64 alternatives and 256 references
  or source-event keys. One free-text field contains at most 128 Ki characters.

### Queued and durable envelopes

A queued event has exactly these required fields:

```yaml
event_key: run/export-data--design--design-author--4/completed
kind: lifecycle
event: role-run-completed
stage: design
actor: router
run_id: export-data--design--design-author--4 # required only for run events
related_events: []       # already-durable prior MEM-* IDs
related_event_keys: []   # earlier events in this outbox
payload: {}
```

`run_id` is the envelope's only optional field. It is required for decision
events, author- or review-origin blocking questions, clarification application,
run reservation and result events, executor waits, and artifact or review
acceptance. A router-origin blocking question forbids `run_id`; it belongs to
the product stage but is not attributed to a role run. `run_id` is forbidden
otherwise. A queued relationship by event key must point to an
earlier event in the same ordered outbox. A relationship by event ID must point
to an event before the outbox's expected head.

A durable event removes `related_event_keys`, resolves them into
`related_events`, and adds exactly `event_id`, `recorded_at`,
`previous_event_sha256`, and `event_sha256`. Related IDs must be unique and
strictly earlier than the durable event. The first event has a null previous
hash; every later event has a valid SHA-256 value.

### Exact event registry

Payload objects contain every listed field and no others. `nullable-*` fields
are required keys whose values may be null. `alternatives` uses the completed
receipt alternative schema. `references`, `finding_ids`, `risks`, and
`affected_paths` are ordered unique string lists.

| Kind | Event | Exact payload fields |
| --- | --- | --- |
| interaction | `initial-request-captured` | `request_path`, `request_sha256`, `verbatim` |
| interaction | `blocking-question` | `question`, `origin`, `resume_purpose` |
| interaction | `user-response` | `verbatim` |
| interaction | `clarification-applied` | `key`, `decision_kind`, `summary`, `rationale`, `alternatives`, `references`, `artifact_path`, `artifact_sha256`, `receipt_sha256` |
| interaction | `user-question` | `verbatim` |
| interaction | `agent-answer` | `answer` |
| interaction | `stage-approved` | `action`, nullable `verbatim`, `artifact_path`, `artifact_sha256`, nullable `review_sha256`, nullable `commit_message` |
| interaction | `revision-feedback-submitted` | `verbatim` |
| interaction | `risk-accepted` | non-empty `risks`, nullable `verbatim` |
| interaction | `specification-collision-selected` | `choice`, nullable `verbatim` |
| interaction | `recovery-selected` | `choice`, nullable `verbatim` |
| interaction | `flow-stopped` | nullable `verbatim` |
| decision | `agent-decision-introduced` | `key`, `decision_kind`, `summary`, `rationale`, `alternatives`, `references`, `artifact_path`, `artifact_sha256`, `receipt_sha256`, `semantic_sha256` |
| decision | `agent-decision-revised` | introduced fields plus `supersedes_event_id` |
| decision | `agent-decision-retired` | `key`, `retired_event_id`, `reason`, `artifact_path`, `artifact_sha256`, `receipt_sha256` |
| lifecycle | `specification-created` | `specification`, `request_path`, `request_sha256`, `log_path` |
| lifecycle | `stage-entered` | nullable `from_stage`, `reason` (`initial`, `approval`, `upstream-return`, or `resume`) |
| lifecycle | `role-run-reserved` | `purpose`, `profile`, `adapter`, nullable `configured_agent`, nullable `requested_model`, nullable `requested_reasoning`, `output_path` |
| lifecycle | `role-run-completed` | `receipt_sha256`, `effective_model`, `effective_reasoning` |
| lifecycle | `role-run-blocked` | `receipt_sha256` |
| lifecycle | `role-run-failed` | `receipt_sha256`, `error` |
| lifecycle | `role-run-interrupted` | `reason` |
| lifecycle | `executor-wait-started` | `wait_seconds` |
| lifecycle | `executor-wait-ended` | `outcome`, nullable `detail` |
| lifecycle | `artifact-accepted` | `path`, `sha256`, `receipt_sha256` |
| lifecycle | `review-accepted` | `path`, `sha256`, `receipt_sha256`, `verdict`, `finding_ids` |
| lifecycle | `automatic-revision-started` | `attempt`, `reason` |
| lifecycle | `automatic-revision-limit-reached` | `attempt` (exactly `3`), `reason` |
| lifecycle | `workflow-integrity-failure` | `problem`, non-empty `affected_paths` |
| lifecycle | `upstream-invalidated` | `from_stage`, earlier `to_stage`, `reason` |
| lifecycle | `checkpoint-commit-completed` | `commit_message`, `audit_event_id` |
| lifecycle | `checkpoint-commit-failed` | `commit_message`, `audit_event_id`, `error` |
| lifecycle | `workflow-resumed` | `reason` |
| lifecycle | `workflow-completed` | `plan_path`, `plan_sha256` |

Interaction actors are fixed by event: user-originated events use `user`,
`agent-answer` uses `router`, and clarification-application events use the
accepting feature role. An author-origin blocking question uses that stage's
owner and its run ID; a review-origin question uses that stage's reviewer and
its run ID; a router-origin question uses `router` and no run ID. Every
lifecycle actor is `router`.
Every decision actor is the owning feature role. A role actor must match its
fixed role stage. `initial-request-captured` uses stage `idea`;
specification creation, collision/recovery selection, workflow resume, and final
completion use `workflow`; other events use their product stage.

Within an outbox, events are sorted by the executable causal rank and then by
kind, event name, and event key. The rank groups are: risk acceptance; input
interaction; specification creation, reservation, or wait start; wait end;
role result; a third-attempt limit or trusted integrity failure; blocking
question or agent answer; artifact/review acceptance; clarification
application; introduced, revised, then retired decisions; ordinary revision,
invalidation, commit, or resume lifecycle; stage entry; and workflow completion.
This makes every batch order deterministic while preserving chronological order
across batches.

Use stage `workflow` only for events that do not belong to one product stage,
such as specification creation or recovery. Re-entry into an earlier stage does
not reorganize the file; later events retain their chronological position and
name the re-entered stage.

### Event evidence

Where applicable, an event records:

- the project-relative artifact or review path;
- the canonical hash of that exact accepted version;
- an artifact item such as `DES-002`, `STEP-001`, a finding ID, or a canonical
  requirement delta reference;
- the role run ID and canonical receipt hash;
- configured profile and concrete adapter;
- configured agent or requested model and reasoning level; and
- effective model and reasoning data only when the host or validated mailbox
  receipt supplies them.

Write `unavailable` for runtime identity data that the selected host cannot
verify. Never infer effective execution data from profile names, model aliases,
chat text, or the router's own runtime.

## Markdown format

`mem-log.md` is canonical Markdown generated by the bundled script. Fixed
metadata labels remain in English; verbatim user content and role-authored
decision text retain their original language.

The file starts with one immutable header:

```markdown
# Stepan Feature Audit Log

- Schema version: `1`
- Specification: `export-data`
- Initial request: `docs/changes/specs/export-data/request.md`
- Initial request SHA-256: `sha256:...`

This file is append-only. Current product truth remains in the verified feature
state and approved specification artifacts.
```

The header and all script-owned syntax use UTF-8 without a BOM and LF line
endings. The header ends with one blank line and is by itself a valid empty log
before the first transaction is flushed. Every event is appended directly
after the preceding canonical blank line and has this common shape:

`````markdown
## MEM-000017 — Agent decision revised

- Recorded at: `2026-08-23T12:34:56Z`
- Stage: `design`
- Kind: `decision`
- Event: `agent-decision-revised`
- Actor: `design-author`
- Event key: `run/export-data--design--design-author--4/decision/DES-002`
- Run: `export-data--design--design-author--4`
- Related events: `MEM-000009`
- Previous event SHA-256: `sha256:...`

### Payload

#### Summary (`summary`)

- Value encoding: UTF-8
- UTF-8 bytes: `31`

```stepan-text
Use an asynchronous export job.
```

...the remaining fields in exact schema order...

### Integrity

- Event SHA-256: `sha256:...`

`````

The title is the exact event name with hyphens replaced by spaces and only its
first character capitalized. `Run`, `Related events`, and `Previous event
SHA-256` are always present and use literal `none` when absent. Payload fields
appear once in the insertion order of the exact event registry. Their headings
contain an English label followed by the executable field name in backticks.
No event-specific payload field is promoted into the common metadata.

Every string value, including paths, hashes, enums, user text, and role text,
uses the shown length-prefixed `stepan-text` fence. The byte count is the exact
length of the represented UTF-8 value, excluding the structural newline before
the closing fence. The renderer chooses a backtick fence whose length is
`max(3, longest_backtick_run_in_value + 1)`. A parser consumes the declared byte
length before accepting the matching closing fence and then requires that
rendering the parsed value reproduces the original bytes. Thus payload text may
contain arbitrary line endings, headings, inline backticks, or Markdown fences
without changing or forging the event structure.

Nullable values use the inline literal `null`. Integers use one canonical
decimal inline value. Ordered lists declare their item count and render each
string under a numbered `Item` heading. Alternatives declare their count and
render numbered `Alternative` entries with length-prefixed `Option` and
`Rejected because` strings. Empty lists retain an explicit zero count.

The event hash rule is non-self-referential: render the complete canonical event
block with `sha256:` plus 64 lowercase zeroes as the fixed `Event SHA-256`
sentinel, then SHA-256 hash those exact UTF-8 bytes. Replace the sentinel with
the resulting lowercase `sha256:<hex>` value for the durable block. Validation
reconstructs the sentinel form and compares the result. `log_sha256` hashes the
complete accepted file bytes exactly as stored; it does not apply the ordinary
artifact newline canonicalizer.

Parsing is bounded by the 10 MiB audit limit before UTF-8 decoding. A parser
validates the schema and hash, re-renders every header and event slice byte for
byte, and rejects any unconsumed or manually appended content.

## User interactions

### Initial request

The first event is `initial-request-captured`. It contains the user's complete
initial idea after removing only the Stepan command prefix and action, matching
`request.md`. Do not replace it with the derived specification ID or a summary.

The idea author later records the accepted normalized framing as a separate
linked event after producing a valid artifact. The router must not semantically
normalize the initial idea on its own.

### Workflow questions and answers

Record a blocking question before presenting it to the user. Persist the
question in primary state and the same atomic state replacement's audit outbox.
Flush it before returning the question.

When the user answers:

1. persist the answer verbatim in primary pending state and queue a
   `user-response` event in the same atomic state replacement;
2. flush that event before dispatching the accepting role;
3. let the role interpret the response under its artifact contract; and
4. after successful role completion, append a linked
   `clarification-applied` event containing the normalized decision and artifact
   evidence.

The raw answer and its accepted interpretation are separate chronological
events. If the role blocks again, the verbatim answer remains recorded but no
accepted normalized interpretation is invented.

Continue to keep completed clarifications in `state.yaml.clarifications` as
durable product inputs. The audit copy does not replace them.

### Deterministic user actions

The router may normalize actions whose meaning is fully defined by the workflow
contract, including approval, approval-and-commit, stop, risk acceptance, and a
finite collision or recovery selection. Record both the user's verbatim text
when free-form text was supplied and the canonical action.

`actor` remains exactly `user`. Do not infer a personal identity from Git,
environment variables, host account data, or chat metadata.

### User questions about an artifact

The `question` checkpoint action becomes product-state read-only but
audit-recording. Append one `user-question` event and one linked `agent-answer`
event without changing the artifact, approval, stage, or checkpoint.

Do not log `status` requests; they are observations rather than decisions.
Log `stop` as an explicit user interaction even though it causes no further
product transition.

## Role decision snapshots

Every successful role receipt keeps `schema_version: 1` and adds one required
`decisions` array. An empty array is valid only when the role made no material
decision under its contract. `blocked` and `failed` receipts retain their exact
existing forms and do not report decisions.

Conceptual completed receipt:

```json
{
  "schema_version": 1,
  "run_id": "export-data--design--design-author--4",
  "status": "completed",
  "output": {
    "path": "docs/changes/specs/export-data/design.md",
    "sha256": "sha256:..."
  },
  "decisions": [
    {
      "key": "DES-002",
      "authority": "agent",
      "kind": "technical",
      "summary": "Use an asynchronous export job.",
      "rationale": "Export generation may exceed the HTTP request lifetime.",
      "alternatives": [
        {
          "option": "Generate the export synchronously.",
          "rejected_because": "Large exports may exceed the request timeout."
        }
      ],
      "references": ["DES-002"],
      "source_event_keys": []
    }
  ]
}
```

Mailbox receipts retain their required executor metadata in addition to
`decisions`.

The reusable executable schema fixes these role-specific kinds and key forms:

| Role | Allowed `kind` | Canonical `key` |
| --- | --- | --- |
| `idea-author` | `framing` | `IDEA-` plus at least three decimal digits |
| `requirements-author` | `classification`, `scope`, `behavior`, `verification` | one exact canonical requirement delta reference |
| `requirements-reviewer` | `verdict`, `finding` | `VERDICT` or `REQ-R-` plus at least three decimal digits |
| `design-author` | `technical`, `compatibility`, `risk` | `DES-` plus at least three decimal digits |
| `specification-reviewer` | `verdict`, `finding` | `VERDICT` or `DES-R-` plus at least three decimal digits |
| `planner` | `dependency`, `ordering`, `rollout`, `verification` | `STEP-` plus at least three decimal digits |

All eight decision fields are required and unknown fields are rejected. Keys
are unique across the full receipt snapshot. Alternatives are exact
`option`/`rejected_because` objects and duplicate alternatives are rejected.
References and source-event keys are ordered and unique. Agent-authority
decisions require an empty `source_event_keys`; user-authority decisions require
at least one valid source event key.

### Decision threshold

Record only choices that materially affect product behavior, scope,
architecture, compatibility, risk, cost, schedule, implementation ordering, or
verification. Do not record private reasoning traces, incidental phrasing,
formatting, trivial naming, or alternatives that were not genuinely considered.

When a material alternative was considered, record the option and the specific
reason it was rejected. An empty alternatives list is valid when no material
alternative was evaluated.

### Authority

- `authority: agent` identifies a role-owned material choice and participates
  in decision snapshot diffing.
- `authority: user` identifies a role's accepted normalization of one or more
  prior user-input events. It must link through `source_event_keys`, does not
  become part of the agent-owned decision index, and must not broaden the
  user's statement.

The router never manufactures either form from an artifact body.

### Full snapshots and diffing

For `authority: agent`, a completed receipt contains the role's complete current
set of material decisions for the accepted output, not merely the decisions
changed during that run. The deterministic helper compares the canonical
snapshot with `audit.decision_index`:

- a new key produces `agent-decision-introduced`;
- an existing key with a different semantic hash produces
  `agent-decision-revised` and links the previous event;
- a previously indexed key absent from the new snapshot produces
  `agent-decision-retired` and links the previous event; and
- an unchanged key produces no duplicate decision event.

The accepted artifact-version lifecycle event is still written even when every
decision is unchanged.

Hash the canonical semantic fields: authority, kind, summary, rationale,
ordered alternatives, and ordered references. Exclude run ID, artifact hash,
timestamps, and other execution metadata so a new artifact version does not
appear to revise an unchanged decision.

The executable semantic preimage is one UTF-8 JSON object with those fields in
that exact order, no ASCII escaping, no insignificant whitespace, and
alternative objects ordered as `option` then `rejected_because`. Strings are
not otherwise normalized; alternative and reference array order is semantic.
The digest is lowercase `sha256:<hex>`. The canonical receipt evidence hash
uses the validated receipt as UTF-8 JSON with recursively sorted object keys,
no ASCII escaping, and no insignificant whitespace.

### Role-specific coverage

- `idea-author`: normally has no agent-authority product decision. It may return
  user-authority normalized framing linked to the initial request and accepted
  clarifications.
- `requirements-author`: use canonical delta references as decision keys for
  material delta classification and behavior decomposition. Any unresolved
  product meaning still requires a user question.
- `requirements-reviewer`: include the verdict and every blocking or advisory
  finding. Finding references and IDs must match the validated review.
- `design-author`: include every `DES-*` exactly once. Decision references must
  exist in the validated design artifact.
- `specification-reviewer`: include the verdict and every blocking or advisory
  finding. Finding references and IDs must match the validated review.
- `planner`: include only material dependency, ordering, rollout, or verification
  choices, with references to existing `STEP-*` entries. Do not duplicate every
  incidental plan step merely to make the list non-empty.

Deterministic validation proves key uniqueness, field shape, reference
existence, and role-specific structural coverage where the artifact has stable
identifiers. The role remains responsible for semantic completeness and
faithfulness.

### Revision inputs

On `purpose: revise`, add the current role-owned artifact to the role manifest's
hashed inputs before dispatch, even though it is also the expected output path:

| Role | Current artifact input and sole allowed output |
| --- | --- |
| `idea-author` | `docs/changes/specs/<spec-id>/idea.md` |
| `requirements-author` | `docs/changes/specs/<spec-id>/requirements.md` |
| `design-author` | `docs/changes/specs/<spec-id>/design.md` |
| `planner` | `docs/changes/specs/<spec-id>/plan.md` |

Order inputs as the role's ordinary workflow inputs, then this current artifact,
then the profile's ordered pinned project inputs. `purpose: draft` does not add
the current artifact. Native and mailbox manifests use the same input records
and order.

Before dispatch, manifest construction reads the current artifact and records
its canonical hash. Manifest validation requires that exact path, order, and
hash and rejects a missing or changed current artifact. The executor must read
those existing bytes before atomically replacing the same path. The path remains
the sole allowed output; `mem-log.md` is neither an input nor an allowed write.

At result acceptance, revalidate every ordinary input against its manifest
hash. The only exception is the exact canonical role-owned path above on
`purpose: revise`: its old bytes were required to pass manifest validation
before dispatch and have now been replaced, so do not compare the old input hash
to the new file. Instead require the receipt output path to be that same
canonical path and verify its new bytes against the receipt output hash. Do not
apply this exception to another input/output alias.

This is required to preserve stable identifiers and return a complete current
decision snapshot. It does not archive the old version or the previous review.
Where existing feedback rules require a previous review, retain its canonical
path and hash plus the ordered unresolved finding IDs. Do not substitute the
audit log for the old artifact or review input.

## Lifecycle events

Record at least these lifecycle facts once feature state exists:

- specification and audit-log initialization;
- entry into or return to a stage;
- durable role-run reservation before dispatch;
- role completion, blocking question, validated failure, interruption, or
  bounded external wait;
- accepted artifact or review path and canonical hash;
- review verdict and referenced findings;
- automatic revision, user-directed revision, and the third-attempt stop;
- approval and explicit risk acceptance;
- checkpoint commit selection and outcome linkage;
- upstream invalidation and return when a later stage exposes an upstream
  defect;
- detected unexpected writes, changed pinned inputs, or other recoverable
  workflow integrity failures when the audit log itself remains trusted;
- explicit stop and later resume; and
- final plan approval and workflow completion.

Do not emit one event for every successful file read, hash comparison, schema
check, mailbox poll, or internal routing step. Fold successful low-level checks
into the lifecycle event whose acceptance depends on them. Record a technical
failure when it changes user impact, recovery, or workflow progression.

### Executor provenance

The run-reservation event records the persisted executor profile, concrete
adapter, configured named agent or requested model, and configured reasoning
level when present. The completion event records effective model and reasoning
only when exposed by the selected native runtime or validated mailbox receipt.

Use `unavailable` rather than guessing an effective value. Preserve requested
and effective values separately.

## Transactional writing

### Queue

Every operation that changes primary workflow state must place its complete,
canonical audit batch in `audit.outbox` within that same atomic `state.yaml`
replacement. The outbox's expected head, file hash, and transaction ID bind the
batch to the exact state from which it was prepared.

The bundled script owns one executable `FEATURE_TRANSITION_TABLE`. It is the
only workflow-branch-to-event mapping and is used by specialized initialization,
reservation, and role-result acceptance plus generic transition dispatch and
batch validation. The private generic operation is:

```text
stepan.py workflow-transition --state <state.yaml> --project-root <root> \
  --spec-id <id> --name <table-name>
```

It reads the table-selected strict JSON object from standard input, rejects
unknown fields, validates the trusted log and empty outbox, and atomically writes
the primary mutation plus the complete ordered batch. Stable run, checkpoint,
wait, or caller-supplied operation identity forms each anchor event key. A retry
after the anchor is durable validates equivalent evidence and returns
`already-applied` without writing; a conflict blocks. The JSON result is private
router data and contains no event IDs, outbox, hashes, or log mechanics.

The table also declares intentionally unlogged protocol outcomes: `status`,
presentation of an already durable checkpoint or completion, a pre-state
missing-idea prompt, a native malformed response awaiting the bounded repair
decision, and an untrusted audit-integrity hard stop. `status` may first finish
an interrupted valid flush, but the observation itself remains unlogged.

`reserve-run` must require an empty outbox, reserve the active run, increment the
run sequence, and queue the run-reservation event in one atomic state write.
The router flushes that event before invoking a native agent or publishing a
mailbox request.

The queued reservation uses event key `run/<run-id>/reserved`, relates to the
current durable head, and records the persisted profile name, concrete adapter,
configured native agent or requested mailbox model/reasoning, and exact output
path. `reserve-run` keeps its existing reservation JSON result; the pending
transaction is read from the newly persisted state and is flushed separately.

Role-result acceptance validates receipt, filesystem effects, input hashes, and
artifact or review structure before clearing `active_run`. The state write that
clears it also applies the table-selected result routing and queues the complete
role-result batch: owner/reviewer checkpoint status, review dispatch status,
review-origin pending decision, automatic revision, or third-attempt stop.

The acceptance primitive is `accept-role-result --state <path>
--project-root <root> --spec-id <id> [--input <path>=sha256:<digest> ...]` with
the completed receipt on standard input. Inputs repeat in exact role-manifest
order. It atomically clears `active_run` and queues the role completion,
artifact or review acceptance, linked user normalizations, and decision diff;
it composes the matching result transition from the executable table in the
same replacement. A changes-required review needs `--review-question <text>`
only when it creates a `user-decision` pending question or reaches automatic
attempt three; other branches reject that option.

`accept-role-result` rejects `status: waiting-executor`. Mailbox completion is
not unsupported: the mailbox transition must first atomically record
`executor-wait-ended` and restore the active run's underlying
`drafting`/`revising`/`reviewing` status, then invoke the same acceptance
primitive. Immediate mailbox completion before entering the wait state may use
the primitive directly.

### Flush

The bundled writer must:

1. validate state, log path, byte length, whole-file hash, head ID, head hash,
   outbox expected head, and every queued event;
2. reject an unknown or conflicting event key;
3. assign consecutive event IDs and `recorded_at` timestamps;
4. resolve queued event-key relationships to event IDs;
5. render canonical Markdown event blocks;
6. construct `new_bytes = old_bytes + rendered_blocks` and verify the old bytes
   are the exact prefix;
7. write and fsync a same-directory temporary file, then atomically replace
   `mem-log.md`;
8. update log metadata, head metadata, next sequence, and decision index and
   clear the outbox in an atomic `state.yaml` replacement; and
9. reread and validate both files before returning success.

The public flush command is:

```text
stepan.py flush-audit --state <state.yaml> --project-root <root> --spec-id <id>
```

It reads no standard input. Its JSON result includes schema version, `status`,
`event_ids`, and the resulting `audit` object. A new append returns status
`flushed` plus the transaction ID; metadata-only crash recovery returns
`recovered` plus the same IDs and transaction ID; a retry after complete success
returns `no-op` and an empty event-ID list.

If the log replacement succeeds but the state update does not, the outbox
remains durable. On retry, the writer recognizes the exact already-appended
event keys and canonical blocks, completes the state metadata update, and does
not append duplicates.

A fresh batch uses one UTC `recorded_at` value for all its events. Recovery first
validates the old prefix using the still-persisted state metadata, then accepts
only a complete suffix with the queued event count, consecutive assigned IDs,
that single batch timestamp, resolved relationships, payloads, hash chain, and
canonical bytes exactly matching the pending outbox. Ordinary `validate-state`
remains strict and rejects the temporary log/state metadata mismatch; only the
flush operation has this narrow recovery read.

Do not dispatch a role or apply another workflow operation between queue and
successful flush.

## Validation and recovery

`validate-state` must also require and validate the exact specification-local
`mem-log.md`. At minimum validate:

- canonical location and regular-file status;
- bounded UTF-8 content;
- immutable header values;
- complete Markdown structure;
- consecutive event IDs;
- event-key uniqueness;
- allowed kind, event, stage, actor, and role values;
- every previous-event link and event hash;
- full-file byte length and hash against state;
- state head and next sequence;
- decision index agreement with active non-retired decision events;
- outbox shape and its expected current head; and
- exact initial-request equivalence with `request.md`.

Use a 10 MiB audit-log size limit, which accommodates accumulated feature
history and is larger than the ordinary 256 KiB control-file limit. Reject
oversized input before parsing and stop rather than truncating history.

Treat these conditions as blocking integrity failures:

- changed, removed, reordered, or truncated prior bytes;
- an unrecognized manual append;
- a broken event or whole-file hash;
- duplicate event keys with different content;
- a state head or decision-index mismatch;
- malformed Markdown or an invalid event schema; or
- an outbox prepared against a different head.

Never repair or rewrite prior log bytes automatically. Report the affected
specification and require explicit recovery outside the normal feature flow.

An immediate `status` action may flush a valid pending outbox left by an
interrupted prior operation as recovery, but it does not add a status event.
If audit integrity cannot be established, status reports the pause without
changing product state.

## Checkpoint commits

`continue-and-commit` continues to commit only
`docs/changes/specs/<spec-id>/`. The approval or commit-selection audit event
must be durable before the Git operation.

Avoid the self-referential requirement to place a commit's own SHA inside a
file contained by that commit. Instead:

- assign the relevant audit event its stable `MEM-*` ID;
- retain the subject `stepan(<spec-id>): approve <stage>`; and
- add the commit trailer `Stepan-Audit-Event: <event-id>`.

The log records the stage, canonical action, and commit message. Git history
provides the commit SHA and the trailer provides the reverse lookup to the audit
event. Do not duplicate or predict the commit SHA in `mem-log.md`.

The bundled private operation is:

```text
stepan.py checkpoint-commit --state <state.yaml> --project-root <root> \
  --spec-id <id>
```

It reads no standard input. It requires a trusted state/log pair, an empty
outbox, the strict primary `checkpoint_commit` intent, and its already durable
`stage-approved` event. It requires the canonical subject from that intent and
uses the writer-assigned event ID in exactly one `Stepan-Audit-Event` trailer.
Before Git it rejects any pre-existing staged delta inside the specification
directory and snapshots the semantic staged delta outside it. It stages the
exact specification directory only as router-owned preparation. It invokes
`pre-commit`, `prepare-commit-msg` with the canonical message file and source
`message`, and `commit-msg` exactly once through `git hook run` with a private
index. Hook failure, an invalid resulting subject or trailer, any hook-staged
path outside the specification, or any changed specification snapshot stops
before ref update. If `git hook run` is unavailable, fail closed. Only after
those checks does it invoke Git's path-limited `--only` commit with an empty
temporary hooks path so no hook is repeated, then invoke `post-commit` exactly
once with a private index. A non-zero `post-commit` exit does not invalidate or
duplicate an otherwise verified commit. Hook-created working-tree changes are
preserved. Unrelated staged intent must remain semantically unchanged.

A reported success additionally requires the new commit to be current `HEAD`,
to have the preflight HEAD as its sole parent, to contain the exact pre-hook
specification tree and no other working-tree changes, and to have the exact
subject and trailer. Only then may the operation apply and flush
`checkpoint-commit-succeeded`. A retry after Git created the commit but before
that state transition may recover only one exact linked commit at current
`HEAD`. If such a commit is reachable but current HEAD has advanced, stop as a
concurrent-history conflict and never create a duplicate.

On commit failure, remain at the checkpoint and append a failure lifecycle
event when the audit log is still valid. Remove router-created specification
staging only when the real index still exactly matches the router's staged
snapshot. Preserve user- or hook-created working-tree and index changes; never
reset, checkout, clean, or restore over them. Never roll back or edit an already
durable audit event. The operation derives consecutive failure attempt identity
from durable failure events for the same selection; primary state remains the
source of retry intent.

## Breaking schema change

This change intentionally provides no compatibility or migration path.

- Keep feature `state.yaml` at `schema_version: 1`, but require the new `audit`
  object and nullable `checkpoint_commit` field and reject old states without
  either.
- Keep role receipts at `schema_version: 1`, but require `decisions` on every
  completed receipt and reject old completed receipts without it.
- Start `mem-log.md` at schema version 1.
- Do not infer or synthesize missing history for an old specification.
- Do not add compatibility branches or a migration command.

Project execution configuration remains schema version 1. Do not add an audit
identity field.

## Acceptance criteria

1. A new specification creates a valid header, first verbatim request event,
   matching `request.md`, and state audit metadata before its first role run.
2. Every accepted log update preserves all previous bytes as an exact prefix.
3. Manual edit, deletion, reorder, truncation, or append is detected before the
   next normal workflow action.
4. A queued but unflushed audit transaction blocks subsequent transitions and
   role dispatch.
5. Recovery before append, after log replacement, and after state replacement
   is idempotent and produces no duplicate event.
6. A blocking question is logged before presentation. Its verbatim answer and
   later accepted normalization are separate linked events.
7. A successful role receipt without `decisions` is rejected. An empty list is
   accepted only under the selected role's structural rules.
8. Design receipts cover every `DES-*`; review receipts cover verdict and all
   findings; every supplied artifact reference is validated.
9. A revised full decision snapshot produces introduced, revised, and retired
   events correctly and does not duplicate unchanged decisions.
10. Material rejected alternatives and their rejection reasons survive artifact
    revision in the log.
11. Revision manifests include the current role-owned artifact as a hashed input
    without granting access to `mem-log.md`.
12. Artifact and review bodies are not archived. Their accepted lifecycle events
    contain canonical paths and hashes.
13. Requested executor identity and available effective runtime identity are
    recorded without inference; unavailable effective data is explicit.
14. `question` logs question and answer without changing product state;
    `status` adds no event; `stop` adds one user-action event.
15. `continue-and-commit` uses a `Stepan-Audit-Event` trailer and does not place
    a predicted or self-referential commit SHA in the log.
16. Old state and old completed receipts are rejected without migration.
17. The audit log remains absent from every role manifest input and allowed
    write.
18. Existing feature workflow validation, receipt repair, mailbox waiting,
    unexpected-write checks, approval eligibility, and stage routing continue
    to work with the new mandatory audit boundary.
19. Bundled self-tests cover rendering, parsing, hash chaining, outbox recovery,
    decision diffing, tamper detection, receipt validation, and representative
    end-to-end stage transitions.
