# Feature specification protocol

## Contents

- [Invariants](#invariants)
- [Command surface](#command-surface)
- [Tooling](#tooling)
- [Audit contract](#audit-contract)
- [Execution](#execution)
- [Router launch boundary](#router-launch-boundary)
- [Storage and schemas](#storage-and-schemas)
- [Role dispatch](#role-dispatch)
- [Routing](#routing)
- [User-facing communication](#user-facing-communication)
- [Interactive choices](#interactive-choices)
- [Checkpoints](#checkpoints)
- [Identifiers and hashes](#identifiers-and-hashes)
- [Failure handling](#failure-handling)

## Invariants

- Select this workflow only from an explicit `$stepan feature <action>` request
  in Codex or `/stepan feature <action>` request in Claude Code. After
  selection, accept a bare response only as the immediate answer to the
  router's missing-idea question, a persisted pending question for the loaded
  specification, or a checkpoint the router just presented.
- Route `idea → requirements → requirements review → design → specification review
  → plan`; stop after plan approval.
- Before feature state exists, use conversation context only to receive the
  immediate answer to a missing-idea question. Once state exists, treat
  repository files, not chat history, as the source of truth.
- Dispatch every author and every review as a fresh role run without inherited
  conversation. Pass only declared skill-resource paths, project-data paths and
  hashes, and feedback.
- Let each role write only its exact declared output. Let only the logical
  router, through the workflow's atomic state operations, write `state.yaml`.
  Only bundled deterministic operations may create, parse,
  validate, or extend `mem-log.md`; roles must never receive it as an input or
  allowed write. Keep artifact bodies out of router context when a path and hash
  are sufficient.
- Treat the verified state and approved artifacts as current product truth.
  Treat `mem-log.md` only as the chronological audit record; never use it to
  reinterpret product intent or reconstruct a role input.
- Couple every product-state mutation with its complete audit batch in the same
  atomic state replacement. Flush a non-empty audit outbox before any later
  transition, reservation, dispatch, approval, commit, or completion. Dispatch
  a role only after its durable reservation event has been flushed.
- Pause on any audit recording or integrity failure. Never continue with an
  unaudited state change, rewrite prior audit bytes, or append to an untrusted
  log.
- Never silently change an approved artifact or infer a product decision. At the
  requirements authoring stage, require every product choice needed by the
  artifact contract to be grounded in declared inputs and ask one question when
  its evidence gate fails.
  At the requirements stage, treat every unresolved semantic uncertainty as
  requiring a user question before authoring, revision, review completion, or
  approval.
- Never implement code, run a code-review flow, push, merge, or deploy.

Declare project inputs only in each profile's strict `project_inputs` list in
`.stepan/config.yaml`. Resolve and hash those files before state creation, pin
them in the execution snapshot, and pass only the selected profile's pinned
entries to a role. Custom-agent instructions may not declare, discover, or
expand project inputs. Do not prescribe repository-wide documents here.

## Command surface

Accept these actions after an explicit Stepan `feature` invocation:

- `new [idea]`: create a feature specification; when the idea is omitted, ask
  for it before writing;
- `resume <spec-id>`: continue the single transition allowed by saved state;
- `status <spec-id>`: show concise product-facing progress and the current
  checkpoint without changing product state or adding an event; it may finish
  one valid interrupted audit-outbox flush before reporting, and includes
  internal state only when the user explicitly asks for diagnostics;
- `continue <spec-id>` and `continue-and-commit <spec-id>`: apply the matching
  checkpoint action;
- `revise <spec-id> <feedback>`, `question <spec-id> <text>`, and
  `accept-risk <spec-id> <finding-ids> [comment]`: apply the matching checkpoint
  action; `question` is product-state read-only but audit-recording;
- `answer <spec-id> <text>`: answer the specification's persisted pending
  question when it is no longer the immediately preceding dialog turn;
- `stop <spec-id>`: make no further product transition after durably recording
  the explicit stop action.

These command forms are the durable interface for starting, resuming, or
addressing a specification outside the immediately preceding interaction. Do
not require them when the user is responding directly to a question or
checkpoint presented under the interactive-choice rules.

Reject an omitted or unknown action without reading feature state or writing.

## Tooling

Use the bundled Python script through `uv` for deterministic identifiers,
configuration and state validation, resource manifests, run reservations,
hashes, artifact and review structure, receipt validation, feature
initialization, role-result acceptance, table-defined workflow transitions,
audit flushing, and checkpoint commits. It emits JSON,
declares no third-party dependencies, and uses only the Python standard
library. The feature-state mutation commands are `initialize-feature`,
`reserve-run`, `accept-role-result`, `workflow-transition`, `flush-audit`, and
`checkpoint-commit`;
each is restricted to
the verified project root, specification, and state paths supplied to it. No
prompt or shell snippet may reproduce audit parsing, rendering, hashing,
extension, decision diffing, or crash recovery:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" spec-id --id-hint <text> --root docs/changes/specs
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" run-id --spec-id <id> --stage <stage> --role <role> --sequence <n>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" approval-transition --action <continue|continue-and-commit> --stage <stage> --status <status> [--review-verdict <pass|changes-required>] [--pending] [--unresolved-user-decision]
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-config --config <config.yaml> --project-root <project-root> --host <codex|claude-code>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-state --state <state.yaml> --project-root <project-root> --spec-id <id>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" initialize-feature --project-root <project-root> --spec-id <id>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" flush-audit --state <state.yaml> --project-root <project-root> --spec-id <id>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" checkpoint-commit --state <state.yaml> --project-root <project-root> --spec-id <id>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" role-resources --skill-root <skill-root> --role <role>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-role-manifest --skill-root <skill-root> --project-root <project-root> --state <state.yaml> --spec-id <id> --adapter <native|mailbox>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" reserve-run --state <state.yaml> --project-root <project-root> --spec-id <id> --stage <stage> --role <role> --purpose <purpose> --executor <profile> --adapter <kind> --output <path> --request-sha256 <hash>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" accept-role-result --state <state.yaml> --project-root <project-root> --spec-id <id> [--input <path>=sha256:<digest> ...] [--review-question <text>]
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" workflow-transition --state <state.yaml> --project-root <project-root> --spec-id <id> --name <table-name>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" hash <file>...
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-artifact --kind <requirements|design|plan> --file <path> [--requirements <path>] [--design <path>]
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-review --file <path> --stage <requirements|design> --input <path=hash>...
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-receipt --run-id <id> --output <path> --adapter <native|mailbox>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-router-result
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" self-test
```

Pass `initialize-feature` one exact JSON object containing `initial_request` and
the normalized `execution` snapshot on standard input. Pass a completed receipt
to `accept-role-result`, and repeat `--input` in the exact validated role-manifest
order. Use `--review-question` only for the table branches that persist a review
user decision or the third automatic-failure question. Pass
`workflow-transition` the exact JSON object selected by the executable table;
never add unknown fields or reproduce its mutations or event construction.
`flush-audit` and `checkpoint-commit` read no standard input. Only invoke
`checkpoint-commit` for a persisted `continue-and-commit` intent after its
selection batch is durable; never reproduce its Git staging, trailer, index, or
recovery logic in shell commands.

Use `reserve-run`, not `run-id`, for workflow dispatch. It reads the saved next
sequence and atomically writes the active run, incremented next sequence, and
reservation audit outbox; keep `run-id` only as a non-workflow identifier helper.
Pass one executor response to `validate-receipt` on
standard input as unchanged UTF-8 without embedding untrusted response text in a
shell command. Do not use a host pipeline whose encoding can replace or
transliterate characters; stop if byte-preserving UTF-8 transfer is unavailable.
For a mailbox completion, also pass `--requested-model` and the configured
optional `--requested-reasoning`.
Pass a dedicated router's complete final response to `validate-router-result`
under the same stdin and encoding rules. Relay only the validated `message` and
use its `continuation` solely to recognize the immediately following reply.

Require an already available `uv` executable and use the exact option order
shown above for every bundled-script call. `--no-project` prevents discovery,
installation, or synchronization of the current project's Python environment;
`--no-python-downloads` prevents an implicit interpreter download. Do not
install `uv` or Python, fall back to `python`, `python3`, or `py`, omit either
isolation flag, or change the launch form during one specification run.

Resolve `<skill-root>` as the canonical directory containing the shared router
`SKILL.md`, following any discovery symlink and compatibility entrypoint link.
It may be inside the project or in the host's user-level skill directory. Keep
it distinct from the canonical project root, require it to be readable, and
resolve every bundled role brief, directly declared resource, and script beneath
it. Never assume a host-specific discovery directory or require the skill root
to be project-relative.

For every read-only command, stop without writing if the script is unavailable,
fails, or returns malformed JSON. If a mutating command fails or its JSON is
lost, reread the exact state and log through the bundled operations. When a
complete outbox is present, invoke only `flush-audit`; do not retry the state
mutation. `validate-state` intentionally rejects the narrow crash window in
which the log replacement is durable but state metadata is stale; only
`flush-audit` may recognize and finish that recovery. Never invoke `reserve-run`
again while `active_run` is non-null, or accept another result while an outbox
is pending. Do not reproduce the script's algorithms in a prompt or shell
one-liner.

## Audit contract

Read [`audit-log-spec.md`](audit-log-spec.md) completely before reading,
creating, or changing feature state, accepting a role result, dispatching a
role, or recovering an interrupted workflow. Treat it as the single normative
contract for the audit state schema, completed-receipt decisions, event
registry, canonical Markdown, append-only integrity, transactional outbox,
provenance, and recovery. This protocol defines when those mechanisms are used
and does not restate their detailed schemas.

The dedicated logical router may queue validated event data only as part of the
workflow operation that owns the corresponding state change. Only the bundled
operations may initialize, parse, render, hash, extend, or validate the log.
Never expose `mem-log.md`, event IDs, hashes, decision-index data, or outbox
contents to roles or ordinary user-facing communication.

## Execution

Read [`execution.md`](execution.md) completely before resolving project
configuration, creating feature state, or dispatching a role. Treat it as the
normative contract for project bindings, native and mailbox adapters, role-run
receipts, bounded waiting, and filesystem verification.

Require `.stepan/config.yaml`, resolve it before writing, and persist the
normalized execution snapshot in feature state. On Codex, direct the user to
`$stepan init codex` when the file is absent. On Claude Code, direct the user to
`/stepan init claude`. Never use a built-in default, and never reread project
execution configuration to rebind an existing specification.

## Router launch boundary

Read [`router.md`](router.md) completely before loading a feature role or
changing feature state. It defines an optional boundary between the primary
Stepan launcher phase and the logical feature router.

Apply the boundary only after the action is known and, for `new`, non-whitespace
idea text is available. Select its source as follows:

- for `new`, use only the current `.stepan/config.yaml` and require its feature
  router binding to resolve to a named native agent on the current host;
- for an existing specification, use only the persisted execution snapshot in
  its verified `state.yaml` and require the same non-null named router binding;
  and
- for an immediate pre-state collision choice, reuse the exact configuration
  path, hash, and router binding from the immediately preceding interaction and
  stop if the file changed.

Follow `router.md` and the selected native adapter for every launch. In
fresh-agent mode, launch exactly the bound named agent, relay its single
user-facing result, and perform no feature transition in the launcher. In
verified main-thread mode, require the current main thread already to be the
bound named agent with matching runtime model settings, end the launcher phase,
and execute only as that logical router. If the required mode cannot be
validated or cannot dispatch fresh role runs, report an unsupported or
misconfigured host runtime and stop without changing feature state. Never fall
back to a null binding, an ordinary primary conversation, `agent: default`,
another agent, or another model. A named agent already operating as the logical
router must skip this launch boundary rather than recursively launching itself.

## Storage and schemas

Keep one change under:

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

Create stage and review files lazily. Create and mutually validate
`request.md`, `mem-log.md`, and `state.yaml` before launching the first role. The
state has this shape. `audit` is a required mapping, but its complete, strict
schema is intentionally not repeated here; construct and validate it only from
`audit-log-spec.md`:

```yaml
schema_version: 1
specification: export-data
created_at: 2026-08-15T12:00:00Z
initial_request_sha256: sha256:...
stage: idea
status: drafting
automatic_revision_attempts: 0
next_run_sequence: 1
approvals: {}
clarifications: []
pending: null
active_run: null
checkpoint_commit: null
audit: <required mapping from audit-log-spec.md>
execution:
  source: project
  config_sha256: sha256:...
  adapters:
    codex:
      kind: codex
  profiles:
    orchestrator:
      adapter: codex
      agent: stepan_orchestrator
      project_inputs: []
    author:
      adapter: codex
      agent: stepan_author
      project_inputs: []
    architect:
      adapter: codex
      agent: stepan_architect
      project_inputs: []
    planner:
      adapter: codex
      agent: stepan_planner
      project_inputs: []
    reviewer:
      adapter: codex
      agent: stepan_reviewer
      project_inputs: []
  bindings:
    router: orchestrator
    idea-author: author
    requirements-author: author
    requirements-reviewer: reviewer
    design-author: architect
    specification-reviewer: reviewer
    planner: planner
```

The example is a snapshot resolved on Codex. On Claude Code, persist
`kind: claude-code` instead. Never persist the unresolved `native` kind.

Allow these values:

- `stage`: `idea`, `requirements`, `design`, `plan`;
- `status`: `drafting`, `reviewing`, `revising`, `awaiting-approval`,
  `awaiting-decision`, `waiting-executor`, `approved`.

Require bundled `validate-state` to accept the file and its exact
specification-local `mem-log.md` before every existing-state action, except the
narrow interrupted-flush recovery owned by `flush-audit`. It enforces
`schema_version: 1`, the `specification` identity and exact state-file location,
a positive `next_run_sequence`, a normalized `execution` snapshot matching the
execution contract, a canonical `initial_request_sha256` matching `request.md`,
unchanged pinned project inputs, the complete audit integrity and metadata
contract, either `null` or one valid `active_run`, and either `null` or one
strict checkpoint-commit intent at an idle approval checkpoint. States without
the required audit object or `checkpoint_commit` field are invalid and have no
compatibility or migration path.
Require `clarifications` to be a list of valid completed clarification records.
Permit `waiting-executor` only with an active mailbox run. Require
`bindings.router` to name the configured native orchestrator. While `active_run`
is non-null, require `next_run_sequence` to equal its `sequence + 1`.

The router creates the immutable request only through `initialize-feature`.
Preserve the user's initial idea text after removing only the Stepan command
prefix and action and pass it unchanged in the initialization input. The
operation creates and mutually validates `request.md`, `mem-log.md`, and
`state.yaml` as one initialization attempt, including the initial request and
lifecycle audit events; on failure it rolls back only paths created by that
attempt. Never revise or delete `request.md`. Use it, rather than chat history
or the audit copy, as the idea author's durable input.

Store one unresolved question or revision request as:

```yaml
pending:
  kind: clarification | revision
  origin: author | review | router
  stage: design
  request: "..."
  response: null
  resume_purpose: draft | revise
```

After a role accepts an answered clarification, preserve it before clearing or
replacing `pending`:

```yaml
clarifications:
  - stage: requirements
    origin: author
    question: "Which users may export data?"
    answer: "Workspace administrators only."
```

Set `resume_purpose` from the run that blocked, or to `revise` for review- and
router-origin questions. Require each clarification to contain one allowed
stage, one allowed origin, and non-empty question and answer strings. Preserve
insertion order and never edit or delete a recorded clarification. These records
are durable product inputs, not approvals.
Their audit events are chronological evidence only and never replace these
records.

## Role dispatch

Use the matching brief both to validate role-owned data and to launch a role:

| Role | Brief | Artifact inputs | Result | Allowed write |
| --- | --- | --- | --- | --- |
| idea-author | `roles/idea-author.md` | immutable request, applicable clarifications, selected profile inputs | `idea.md` | expected `idea.md` only |
| requirements-author | `roles/requirements-author.md` | approved idea as binding framing; request and clarifications as evidence; selected profile inputs | `requirements.md` | expected `requirements.md` only |
| requirements-reviewer | `roles/requirements-reviewer.md` | request, approved idea, clarifications, requirements, selected profile inputs, previous review on re-review | `review/requirements.yaml` | expected review file only |
| design-author | `roles/design-author.md` | approved idea as binding framing, approved requirements, request and clarifications as evidence, selected profile inputs, previous review on revision | `design.md` | expected `design.md` only |
| specification-reviewer | `roles/specification-reviewer.md` | request, approved idea, clarifications, requirements, design, selected profile inputs, previous review on re-review | `review/design.yaml` | expected review file only |
| planner | `roles/planner.md` | all approved artifacts and selected profile inputs | `plan.md` | expected `plan.md` only |

Resolve each selected brief's `Resources` section with bundled
`role-resources` before validation or launch. Use every direct Markdown link in
written order. Require every resolved path to be under the skill's
`references/modules/` directory. Reject missing, ambiguous, external, or
recursive references. Do not follow links from a resolved resource. Do not load
an unselected resource.

Treat files under `references/modules/` as private role dependencies. Reject a
module if it contains skill frontmatter, defines a direct command or workflow
transition, expands the role's write boundary, or attempts to load another
module. Modules may define only reusable artifact, shared, authoring, consuming,
or reviewing rules for the selected role.

Construct the common role-run manifest defined by `execution.md`, including the
run ID, brief path, ordered `resources`, project-data `inputs` with canonical
hashes, applicable revision `feedback`, expected output, allowed write, and the
exact receipt forms from the execution contract. Pass skill resources by path
only after validating their location and structure; do not compute or persist
their content hashes. Include the ordered clarification history relevant to the
role's declared inputs, plus current findings, the previous review and its
unresolved IDs, or a persisted pending response when the transition requires
them. Let the selected profile read files directly; do not quote their bodies
merely to relay them. Instruct the role not to inspect parent chat or undeclared
runtime data. On `purpose: revise`, include the current role-owned artifact as
the final ordinary workflow input before pinned profile inputs, as required by
`execution.md`. Never include `state.yaml` or `mem-log.md`, and never grant the
role permission to write either. Do not load or reference any unrelated role
brief or resource.

Give a role a sandbox restricted to its one output when available. Only when an
exact write sandbox is unavailable, snapshot the project filesystem before the
role run, verify it after completion, and reject every unexpected change. A
reviewer requires write access to its exact review file and no other path.

When a role's selected rules permit a blocking question, accept only the valid
`blocked` JSON receipt defined by the execution contract and only if the role
made no file changes. Reject a raw question as a malformed receipt. Never
reinterpret a question as an artifact, finding correction, or role failure.
Atomically apply the selected pending-state change and queue the linked
run-result and blocking-question events; flush them before presenting the
question.

## Routing

### Initial idea dialog

For `new` without non-whitespace idea text, ask one direct question requesting
the idea and stop without reading project configuration or writing any file.
Treat only the user's immediate bare reply as the idea for that same `new`
transition. If the reply is empty or is itself a question instead of an idea,
answer when possible and ask again without writing. A later or ambiguous reply
requires a new explicit `feature new` invocation.

After state exists, the idea author must return a valid `blocked` receipt
containing one question whenever the idea evidence gate fails. The requirements
author may return the same receipt for unresolved requirements decisions.
Persist its question in `pending` and queue the run-result and
`blocking-question` audit records in the same atomic state replacement. Flush
them before presenting only the question to the user; keep `request.md`
unchanged. Treat the user's immediate bare reply as its answer; otherwise
require `answer <spec-id> <text>`. Persist the response verbatim and queue its
`user-response` event atomically, then flush before launching a fresh owner with
the applicable artifacts, prior clarification history, and that response. If it
returns another valid blocked receipt, first archive the answered pending item
in `clarifications`, then replace it with the new pending question and queue the
corresponding accepted-normalization or blocking events as applicable. Do not
create `idea.md` or `requirements.md` until their respective author can satisfy
its complete evidence gate without another material product decision.

For `new`:

1. Resolve execution configuration under the execution contract and stop on any
   invalid or unavailable explicit binding.
2. Derive one `id_hint` from the complete initial idea under the identifier
   rules, then generate `spec-id` and collision data with `scripts/stepan.py`.
   Use an uncollided result without announcing the hint or calculation. On
   collision, present only the choice between resuming `spec_id` and creating
   `next_available` under the interactive-choice rules before writing.
3. Pass the exact initial request and resolved execution snapshot to bundled
   `initialize-feature`. Accept only its mutually validated `request.md`,
   `mem-log.md`, and `state.yaml`; do not hand-create or repair one member of the
   triple. If initialization cannot complete, rely on its scoped rollback and
   stop.
4. When a collision choice was made, record the user's verbatim selection and
   canonical choice in the selected existing or newly created specification and
   flush it before another transition.
5. Reserve a fresh idea-author run, flush its reservation event, and dispatch it
   through the bound adapter with `request.md` by path and canonical hash.

For an existing change, validate state, the complete audit log, and all recorded
hashes before acting. If a valid outbox remains from an interrupted operation,
finish it idempotently with `flush-audit` before interpreting or applying any
later product transition. `status` may perform that recovery and then report
without adding an event. If the pending transaction or log cannot be validated,
pause without changing product state or attempting an append.

If `active_run` is non-null after any required flush, reconcile that exact run
before applying a normal status transition: inspect or wait for the existing
host subagent when available, or inspect the matching mailbox response. When an
interrupted host run cannot be inspected, preserve it and require an explicit
recovery decision; record and flush that choice before abandonment or retry.
Never dispatch a second run while one is active. Otherwise apply the single
matching transition. Every accepted bullet below means one atomic primary-state
mutation plus its complete audit batch, followed by a successful flush before
the next dispatch, transition, checkpoint, or user-facing result:

An explicit resume after a recorded stop or recoverable interruption queues the
canonical recovery choice and workflow-resumed lifecycle evidence and flushes
them before continuing. A resume that merely finishes an interrupted outbox
does not duplicate the state change or its event batch.

- `drafting`: dispatch the current stage owner;
- `waiting-executor`: inspect the active mailbox run; accept its response or
  leave the same run pending without redispatching;
- owner returns a valid `blocked` receipt: set `awaiting-decision`, store its
  question in `pending` with `kind: clarification` and `origin: author`, and
  stop only after its linked run-result and question evidence is durable;
- successful idea author: set `awaiting-approval`;
- structurally valid successful requirements author: set `reviewing` and
  dispatch the requirements reviewer;
- structurally valid successful design author: set `reviewing` and dispatch the
  specification reviewer;
- structurally valid successful planner: set `awaiting-approval`;
- structurally valid review `pass`: set `awaiting-approval`;
- approval of `idea`: advance to `requirements`, set `drafting`, and dispatch the
  requirements author;
- approval of `requirements`: advance to `design`, set `drafting`, and dispatch
  the design author;
- approval of `design`: advance to `plan`, set `drafting`, and dispatch the
  planner;
- approval of `plan`: set `approved`, record workflow completion with the
  accepted plan evidence, and stop only after both are durable;
- review contains a blocking `resolution: user-decision` finding: set
  `awaiting-decision`, store its direct question in `pending` with
  `kind: clarification` and `origin: review`, and stop without automatic
  revision after the review-result and question evidence is durable;
- repairable `changes-required` whose blocking findings all use
  `resolution: author-revision`: increment `automatic_revision_attempts`, set
  `revising`, record the automatic revision start, dispatch a fresh owner with
  findings, then review again;
- third unsuccessful automatic revision: set `awaiting-decision`, store one
  direct next-step question in `pending` with `kind: revision` and
  `origin: router`, record the reached limit and blocking question, and do not
  launch a fourth;
- `awaiting-approval` or `awaiting-decision`: present one user-facing checkpoint
  question and stop;
- `stage: plan`, `status: approved`: give one short completion outcome and stop.

Before each dispatch, require a valid log and an empty outbox, then invoke
bundled `reserve-run` once against verified state; do not edit `active_run`,
`next_run_sequence`, or its reservation event directly. Reread its atomic state
replacement, require it to match the returned reservation, flush the outbox,
and revalidate state. Invoke the bound adapter only after the exact
`role-run-reserved` event is durable. A reservation without a successful flush
is not dispatch permission.

Validate every executor response with the bundled receipt validator. For a
completed receipt, verify the fallback project snapshot when one was required,
then invoke `accept-role-result` with the unchanged completed receipt and the
exact ordered manifest input hashes. Treat its deterministic validation of the
receipt, current inputs, output, artifact or review, decision coverage, durable
reservation evidence, canonical receipt hash, and table-selected result routing
as one acceptance boundary. Supply `--review-question` only for a
changes-required review that creates a user-decision checkpoint or reaches the
third automatic failure. The command atomically clears `active_run`, applies the
matching status, pending, clarification, and retry-counter mutation, and queues
the run completion, artifact-or-review acceptance, accepted user
normalizations, decision diff, and any blocking or automatic-revision event.
Flush that batch before the next action. A valid receipt alone never proves a
valid artifact or authorizes a transition.

For a blocked receipt, require no file changes before applying table transition
`role-blocked`, which atomically records its result and pending question. For a
valid failed receipt use `role-failed`; for a native interruption use
`native-run-interrupted`. These transitions apply the protocol's durable
run-state policy and record the corresponding lifecycle outcome whenever the
audit log remains trusted. For an invalid receipt, use at
most one same-agent format repair when the selected native adapter defines it;
otherwise preserve the run and stop. Receipt repair may change only the response
format and may not invent decisions or executor provenance. For a mailbox run
already in `waiting-executor`, first apply and flush its exact table outcome:
`mailbox-wait-completed`, `mailbox-wait-timeout`,
`mailbox-response-malformed`, or `mailbox-wait-interrupted`. Completion,
malformed response, and interruption restore the active run's underlying status;
timeout leaves the same run in `waiting-executor`. Invoke completed-result
acceptance only after the completed outcome is durable.

When the user answers `pending` through an allowed immediate bare reply or an
explicit `answer` action, persist the answer, set status to `drafting` for
`resume_purpose: draft` or `revising` for `resume_purpose: revise`, and then
queue its verbatim response event in that same atomic state replacement. Flush
it before reserving a fresh role run. Keep the answered pending record until that role
successfully processes it. For `origin: author`, relaunch that owner with the
answer. For `origin: review`, launch the current stage owner with the review and
answer, then review the resulting artifact again. For `origin: router`, treat
the answer as user-directed revision guidance and launch the current stage
owner. Every review-driven revision receives the previous review path/hash and
unresolved finding IDs; the next review receives the same previous review
evidence. Clear `pending` only after the owner processes the answer successfully;
archive an answered clarification in `clarifications` before clearing or
replacing it.
Keep an answered revision request out of `clarifications`. Reset the automatic
revision counter after a user-directed revision or stage transition.

If a later stage exposes a material upstream defect, return to that artifact's
owner, remove approvals for it and all downstream artifacts, and preserve
downstream files as stale until their input hashes are reviewed again. Queue the
upstream invalidation, stage return, and any user input that caused it with that
state change; flush before dispatching the earlier owner. Never treat downstream
files or historical audit decisions as current after their upstream inputs were
invalidated.

## User-facing communication

Treat the workflow's orchestration as private implementation detail, subject to
any progress, tool, or safety notice the host itself requires.

- Do not narrate resource loading, configuration resolution, stages, roles,
  subagents, dispatches, state values, hashes, run IDs, snapshots, receipts,
  verification, automatic revision attempts, log writes, outbox flushes, event
  IDs, or recovery internals. Never relay a role receipt or expose the audit log
  to the user unless explicit diagnostics require the minimum detail needed for
  safe recovery.
- While the workflow can continue safely without user input, continue without
  an ordinary chat update and never pause solely to announce progress.
- Produce a workflow-authored user-facing message only when the workflow needs
  an answer, choice, approval, or recovery action; the user explicitly requested
  `status` or `question`; the workflow completed; or it cannot safely continue.
- When user input is required, present one compact interaction flow containing
  one question at a time, only the product context needed to answer it, and the
  valid response choices or form. Do not prepend the internal reason for the
  pause, such as a role name, state value, transition, or retry count.
- At an approval checkpoint, identify the artifact to review and open one
  product-facing choice flow covering the allowed actions: approve and continue,
  approve and commit, request changes, ask about the artifact, or stop. Do not
  expose artifact hashes, review-file paths, approval records, or canonical
  command tokens.
- On successful completion, give one short outcome and the specification or
  artifact location needed for later use, only after the completion audit batch
  is durable. Do not recap the execution or audit history.
- On failure, state the user impact and recovery action concisely. Include an
  internal identifier or technical detail only when it is necessary to recover
  safely or identify affected data. Ask one direct question when a user decision
  can unblock the workflow; do not invent a question for a terminal failure.
- For `status`, summarize product artifacts already prepared, the decision or
  work currently pending, and the next user action. Do not dump `state.yaml` or
  executor or audit details unless the user explicitly requests diagnostics.
  A successfully recovered outbox does not add a status event or require an
  audit-mechanics announcement.
- If the host requires a progress notice, keep workflow-authored wording
  outcome-oriented and omit private mechanics.

## Interactive choices

Use a host-native structured selection UI when the workflow presents a complete
finite set of choices and that capability is callable in the current host mode.
Feature-detect the capability at the moment of interaction. Do not switch host
or collaboration mode, install or start another tool, open a browser, or fail
the workflow merely to obtain an interactive menu.

Treat the menu as a presentation layer over existing transitions:

- Offer only actions valid for the current verified state. Use short
  product-facing labels and descriptions in the user's language, never
  canonical command tokens, specification IDs already implied by context, or
  internal state names.
- Map each terminal selection to exactly one allowed canonical action. Do not
  write state, approve, commit, revise, or dispatch a role until that terminal
  selection and any required text have been received.
- When all terminal actions do not fit the host's option limit, use a shallow
  menu of non-mutating groups. For an approval checkpoint, group them as
  `Approve`, `Discuss or change`, and `Stop`; then offer `Continue`, `Continue
  and commit`, or `Back` under approval, and `Request changes`, `Ask a question`,
  or `Back` under discussion. `Back` returns to the preceding menu without a
  state change. Omit or replace any action invalid at the current checkpoint.
- After `Request changes`, `Ask a question`, or an action requiring finding IDs
  or a comment, ask for the required free text before mapping the interaction to
  `revise`, `question`, or `accept-risk`. A group selection alone is never an
  action or approval.
- Treat a host-provided free-form or `Other` response as an immediate bare reply.
  Normalize it only when it unambiguously identifies one valid action; otherwise
  ask one follow-up without writing.

When no structured selection capability is callable, show the same choices as a
short numbered list. Accept an immediate ordinal, exact label, or unambiguous
natural-language equivalent without requiring the Stepan command, action token,
or specification ID. If the response is ambiguous, repeat only the unresolved
choice and do not write. Require an explicit command again when the response is
not immediate, identifies another specification, or resumes the workflow from a
later conversation turn.

Do not manufacture a menu for an open-ended product question. Ask for free text
unless the protocol explicitly defines the complete set of mutually exclusive
answers.

## Checkpoints

Keep only these canonical transitions; their tokens are not required as the
user-facing labels of an immediately presented choice:

- `continue`: approve the current artifact, durably record the approval, and
  advance;
- `continue-and-commit`: approve, durably record the approval and commit
  selection, then create the limited checkpoint commit;
- `revise <feedback>`: reset the automatic revision counter, persist the
  canonical router revision request plus the verbatim feedback as an answered
  `pending` revision input, and dispatch a fresh owner followed by review where
  applicable; record the same feedback in the audit trail before dispatch and
  clear the primary pending input only after successful owner acceptance;
- `question <text>`: keep product state, artifacts, approval, stage, and
  checkpoint unchanged, while recording the user's question and the router's
  linked answer before returning it;
- `answer <text>`: supply the answer to the one persisted pending question;
- `accept-risk <finding-ids> [comment]`: record explicit acceptance of an
  objective risk in product approval state and the audit trail, then advance;
- `stop`: durably record the explicit stop and make no further product
  transition.

Normalize a terminal menu selection or an immediate fallback reply to exactly
one of these transitions before applying its existing validation rules. Never
interpret a category selection, silence, an ambiguous label, or a request for
more information as approval.

Before `continue`, `continue-and-commit`, or `accept-risk`, derive the checkpoint
facts from verified state and the applicable review, then apply table transition
`approve-stage`. `status` must be `awaiting-approval`, `pending` and
`checkpoint_commit` must be null, and no `user-decision` finding may be
unresolved. An idea is eligible only after its durable artifact acceptance;
requirements and design need the matching durable accepted review; and a plan
needs its durable artifact acceptance. Ordinary approval requires a passing
review. `accept-risk` instead requires a changes-required review whose blocking
findings are all `author-revision`, and its sorted finding IDs must cover that
set exactly; it advances with approval action `continue` and never selects a
commit in the same input. Treat any script rejection as an invalid checkpoint
and do not approve, commit, or advance. Also require a trusted audit log and an
empty outbox before accepting the action.

Require `revise` or explicit `accept-risk` for blocking `author-revision`
findings. Never allow `accept-risk`, either approval action, or automatic
revision to bypass a `user-decision` finding or unanswered `pending` question.
Silence never approves.

Allow `answer` only when `pending` is non-null. An immediate bare answer and an
explicit `answer` command have the same transition semantics; never interpret
either as approval. Keep `question` product-state read-only and distinct from
`answer`, but atomically queue and flush its `user-question` and linked
`agent-answer` records before replying.

Record each approval under `approvals.<stage>` with `artifact_sha256`, the
applicable `review_sha256`, and `accepted_risks`. After plan approval, keep
`stage: plan` and set `status: approved`. Queue the canonical approval, accepted
risk when applicable, stage entry, and final completion events with their
owning mutations under the audit contract. Do not announce advancement or final
completion until the required batches are durable.

On `continue-and-commit`, the table transition first persists strict
`checkpoint_commit` intent and the approval/selection outbox in one state
replacement. Flush it and resolve the writer-assigned event ID from the durable
`selection_event_key`; never recover intent from `mem-log.md` or predict an ID.
Then invoke bundled `checkpoint-commit`; do not invoke Git directly. It rejects
pre-existing staged changes inside `docs/changes/specs/<spec-id>/`, preserves the
exact staged meaning of every unrelated path, commits only that directory with
subject `stepan(<spec-id>): approve <stage>` and the required audit trailer, and
verifies the resulting HEAD, parent, message, and exact tree before applying the
success primitive. It may recover only an exact matching commit at current
`HEAD`; a matching historical commit below another HEAD is a concurrent-history
hard stop. Do not predict a commit SHA into the audit log or advance before the
bundled operation verifies success. A failed outcome keeps the same primary
checkpoint intent for an explicit retry and preserves user- or hook-created
working changes. The operation runs the repository's commit hooks exactly once
through its isolated hook boundary; never pre-run, bypass, or repeat them from
the router.

## Identifiers and hashes

Treat JSON returned by `scripts/stepan.py` as the sole source of truth for
generated identifiers, run reservations, canonical hashes, and validated
configuration/state structures, resource manifests, artifacts, reviews, and
receipts. Do not reproduce, adjust, or second-guess its computations.

Use canonical hashes for project data only: the immutable request, generated
artifacts and reviews, declared project inputs, and persisted configuration
when the execution contract requires it. Validate briefs, directly linked
resources, and bundled scripts as skill resources by canonical path, existence,
readability, and applicable structure rules; do not hash them to pin a content
version for a role run.

Before calling `spec-id`, derive exactly one ephemeral `id_hint` from the full
initial idea after removing the Stepan command prefix and action:

- express the primary product capability or outcome rather than copying the
  request's opening words;
- normally use two to five short English content words; a single established
  product name or acronym is sufficient, and retain a product-significant
  number when useful;
- prefer an explicit concise feature name supplied by the user;
- omit conversational framing, actors that do not distinguish the feature,
  implementation detail, and any scope not stated by the user.

Treat naming as a non-product decision: do not ask the user to approve the hint,
persist it as a separate artifact, or let it replace or alter `request.md`. Pass
the hint unchanged through `--id-hint`; let the script alone normalize it,
enforce the identifier length, and resolve collisions.

Accept `spec-id` output only when it contains non-empty string fields `spec_id`
and `next_available` plus a boolean `collision`. When `collision` is false, use
`spec_id`. When it is true, present the two identifiers through the
interactive-choice rules and ask whether to resume `spec_id` or create
`next_available`; do not invent another identifier.

Accept `hash` output only when its `files` array contains exactly one entry for
every requested file. Require each entry to contain a string `path` and a
`sha256` value formatted as `sha256:` followed by 64 lowercase hexadecimal
characters. Record and compare the returned digest unchanged.

Accept `reserve-run` output only when it contains exactly the non-empty
string `run_id`, positive integer `sequence`, and positive integer
`next_run_sequence`. Require `sequence` to equal the saved next sequence,
`next_run_sequence` to equal `sequence + 1`, and `run_id` to equal
`<spec-id>--<stage>--<role>--<sequence>`. Never invent, edit, or reuse a run ID
or calculate the next sequence independently.

## Failure handling

- On a role failure or interruption, preserve durable state, queue and flush the
  corresponding lifecycle outcome when the audit log remains trusted, and stop.
  A later explicit resume must inspect any persisted active run before launching
  a fresh role. An invalid native response may receive only the selected
  adapter's one same-agent format repair before this stop rule applies; the
  repair cannot add decisions or provenance that the original run did not
  return.
- On a mailbox timeout, atomically set `waiting-executor` and queue the bounded
  wait lifecycle evidence, flush it, preserve the request and active run, and
  stop without treating ordinary waiting as failure. On resume, record and flush
  the wait outcome before accepting a response or reporting another bounded
  wait.
- On a late response for an abandoned, completed, or unknown run, do not apply
  it or modify repository state. Mention it only when it affects the requested
  action, and keep run details for explicit diagnostics.
- On an unexpected write, project-data hash mismatch, deterministic structural
  validation failure, malformed file, unknown schema, or manual change to an
  approved artifact, do not accept, overwrite, reset, or commit. When state can
  be updated safely, set `awaiting-decision`, persist one direct question with
  `kind: clarification` and `origin: router`, and queue the trusted
  workflow-integrity and blocking-question evidence in the same replacement.
  Flush before presenting only the affected product data and recovery choice
  needed to answer it. Keep the exact technical discrepancy for explicit
  diagnostics unless it is needed to identify affected data safely.
- On any malformed, changed, truncated, manually appended, hash-mismatched, or
  otherwise untrusted `mem-log.md`, audit state, or outbox, stop without changing
  product state and without appending an integrity-failure event to the
  untrusted log. Require explicit recovery outside the normal feature flow.
- On any failure to queue or flush required audit evidence, pause the workflow.
  Never downgrade it to a warning or continue with an unaudited dispatch,
  approval, transition, commit, or completion.
- On commit failure, remain at the same checkpoint. Remove only router-created
  temporary/index changes; never roll back user- or hook-created files. When the
  audit log remains trusted, queue and flush the commit-failure lifecycle result
  without editing the already durable approval or commit-selection event.
- Ask one blocking question at a time. Persist and flush it before presentation;
  never turn uncertainty into approval.
