# Feature specification protocol

## Contents

- [Invariants](#invariants)
- [Command surface](#command-surface)
- [Tooling](#tooling)
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
- Route `idea → requirements → design → plan`; stop after plan approval.
- Before feature state exists, use conversation context only to receive the
  immediate answer to a missing-idea question. Once state exists, treat
  repository files, not chat history, as the source of truth.
- Dispatch every author and every review as a fresh role run without inherited
  conversation. Pass only declared skill-resource paths, project-data paths and
  hashes, and feedback.
- Let each role write only its exact declared output. Let only the router write
  `state.yaml`. Keep artifact bodies out of router context when a path and hash
  are sufficient.
- Never silently change an approved artifact or infer a product decision. At the
  idea stage, require every product choice needed by the artifact contract to be
  grounded in declared inputs and ask one question when its evidence gate fails.
  At the requirements stage, treat every unresolved semantic uncertainty as
  requiring a user question before authoring, revision, review completion, or
  approval.
- Never implement code, run a code-review flow, push, merge, or deploy.

Keep project inputs and their discovery rules in each role's project-scoped
executor configuration. For a host-native executor, keep those rules in its
custom agent. Do not prescribe repository-wide project documents here. Load
only the inputs declared for the role being run.

## Command surface

Accept these actions after an explicit Stepan `feature` invocation:

- `new [idea]`: create a feature specification; when the idea is omitted, ask
  for it before writing;
- `resume <spec-id>`: continue the single transition allowed by saved state;
- `status <spec-id>`: show concise product-facing progress and the current
  checkpoint without changing it; include internal state only when the user
  explicitly asks for diagnostics;
- `continue <spec-id>` and `continue-and-commit <spec-id>`: apply the matching
  checkpoint action;
- `revise <spec-id> <feedback>`, `question <spec-id> <text>`, and
  `accept-risk <spec-id> <finding-ids> [comment]`: apply the matching checkpoint
  action;
- `answer <spec-id> <text>`: answer the specification's persisted pending
  question when it is no longer the immediately preceding dialog turn;
- `stop <spec-id>`: make no further transition.

These command forms are the durable interface for starting, resuming, or
addressing a specification outside the immediately preceding interaction. Do
not require them when the user is responding directly to a question or
checkpoint presented under the interactive-choice rules.

Reject an omitted or unknown action without reading feature state or writing.

## Tooling

Use the bundled Python script through `uv` for deterministic identifiers, run
reservations, hashes, receipt validation, and project initialization. It emits
JSON, declares no third-party dependencies, and uses only the Python standard
library. Only `reserve-run` writes feature state, restricted to the verified
`state.yaml` passed to it:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" spec-id --id-hint <text> --root .stepan/specs
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" run-id --spec-id <id> --stage <stage> --role <role> --sequence <n>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" reserve-run --state <state.yaml> --spec-id <id> --stage <stage> --role <role> --purpose <purpose> --executor <executor> --adapter <kind> --output <path> --request-sha256 <hash>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" hash <file>...
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-receipt --run-id <id> --output <path> --adapter <native|mailbox>
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" validate-router-result
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" self-test
```

Use `reserve-run`, not `run-id`, for workflow dispatch. It reads the saved next
sequence and atomically writes both the active run and incremented next sequence;
keep `run-id` only as a compatibility helper. Pass one executor response to
`validate-receipt` on
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
resolve every bundled role, contract, module, and script beneath it. Never
assume a host-specific discovery directory or require the skill root to be
project-relative.

For every read-only command, stop without writing if the script is unavailable,
fails, or returns malformed JSON. If `reserve-run` fails or its JSON is lost,
reread `state.yaml`: continue only when one complete reservation can be verified
there, otherwise stop. Never invoke `reserve-run` again while `active_run` is
non-null. Do not reproduce the script's algorithms in a prompt or shell one-liner.

## Execution

Read [`execution.md`](execution.md) completely before resolving project
configuration, creating feature state, or dispatching a role. Treat it as the
normative contract for project bindings, native and mailbox adapters, role-run
receipts, bounded waiting, and filesystem verification.

If `.stepan/config.yaml` is absent, use the built-in native binding defined by
the execution contract. If it exists, resolve it before writing and persist the
normalized execution snapshot in feature state. Never reread project execution
configuration to rebind an existing specification.

## Router launch boundary

Read [`router.md`](router.md) completely before loading a feature role or
changing feature state. It defines an optional boundary between the primary
Stepan conversation and the logical feature router.

Apply the boundary only after the action is known and, for `new`, non-whitespace
idea text is available. Select its source as follows:

- for `new`, use only the current `.stepan/config.yaml`, with no router when the
  file is absent or its optional feature router binding is null;
- for an existing specification, use only the persisted execution snapshot in
  its verified `state.yaml`; and
- for an immediate pre-state collision choice, reuse the exact configuration
  path, hash, and router binding from the immediately preceding interaction and
  stop if the file changed.

Treat a missing router field in an older valid schema-version-3 execution
snapshot as null. Never apply a newly configured router to an existing
specification whose snapshot has no router binding.

When the selected binding is null, the current primary conversation remains the
logical router and follows this protocol directly. When it is non-null, follow
`router.md`: launch exactly the bound named agent, relay its single user-facing
result, and perform no feature transition in the primary conversation. A named
agent launched under that contract is already the logical router and must skip
this launch boundary rather than recursively launching itself.

## Storage and schemas

Keep one change under:

```text
.stepan/specs/<spec-id>/
├── state.yaml
├── request.md
├── idea.md
├── requirements.md
├── design.md
├── plan.md
└── review/
    ├── requirements.yaml
    ├── design.yaml
    └── plan.yaml
```

Create stage and review files lazily. Create `state.yaml` before launching the
first role:

```yaml
schema_version: 3
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
execution:
  source: builtin
  config_sha256: null
  adapters:
    native:
      kind: codex
  executors:
    native-default:
      adapter: native
      agent: default
  bindings:
    router: null
    framer: native-default
    specifier: native-default
    designer: native-default
    planner: native-default
    reviewer: native-default
```

The example is a snapshot resolved on Codex. On Claude Code, persist
`kind: claude-code` instead. Never persist the unresolved `native` kind.

Allow these values:

- `stage`: `idea`, `requirements`, `design`, `plan`;
- `status`: `drafting`, `reviewing`, `revising`, `awaiting-approval`,
  `awaiting-decision`, `waiting-executor`, `approved`.

Require `schema_version: 3`, a positive `next_run_sequence`, a normalized
`execution` snapshot matching the execution contract, a canonical
`initial_request_sha256` matching `request.md`, and either `null` or one valid
`active_run`. Require `clarifications` to be a list of valid completed
clarification records. Permit `waiting-executor` only with an active mailbox
run. While `active_run` is non-null, require `next_run_sequence` to equal its
`sequence + 1`. Persist the active run and returned next sequence together from
one valid `reserve-run` result, then reread and verify both before dispatch.
For snapshots created after router-binding support, persist `bindings.router`
explicitly as either null or one executor name. Accept its omission only from an
otherwise valid older schema-version-3 snapshot and normalize that omission to
null in memory without rewriting state merely for migration.

The router writes `request.md` once when creating the specification. Preserve
the user's initial idea text after removing only the Stepan command prefix and
action; encode it as UTF-8 with the canonical trailing newline used by the hash
helper. Never revise or delete it. Use it, rather than chat history, as the
framer's durable input.

Store one unresolved question or revision request as:

```yaml
pending:
  kind: clarification | revision
  origin: author | review | router
  stage: design
  request: "..."
  response: null
```

After a role accepts an answered clarification, preserve it before clearing or
replacing `pending`:

```yaml
clarifications:
  - stage: idea
    origin: author
    question: "Which users may export data?"
    answer: "Workspace administrators only."
```

Require each clarification to contain one allowed stage, one allowed origin,
and non-empty question and answer strings. Preserve insertion order and never
edit or delete a recorded clarification. These records are durable product
inputs, not approvals.

## Role dispatch

Use the matching brief both to validate role-owned data and to launch a role:

| Role | Brief | Artifact inputs | Result | Allowed write |
| --- | --- | --- | --- | --- |
| framer | `roles/framer.md` | `request.md` | `idea.md` | expected `idea.md` only |
| specifier | `roles/specifier.md` | approved `idea.md` | `requirements.md` | expected `requirements.md` only |
| designer | `roles/designer.md` | approved `idea.md`, `requirements.md` | `design.md` | expected `design.md` only |
| planner | `roles/planner.md` | all approved artifacts | `plan.md` | expected `plan.md` only |
| reviewer | `roles/reviewer.md` | current review artifacts; previous review when needed | `review/<stage>.yaml` | expected review file only |

Resolve each selected brief's `Contracts` section before validation or launch.
Load only direct Markdown links that apply to the current stage. Require every
resolved path to be either under this workflow's `contracts/` directory or the
skill's `references/modules/` directory. Reject missing, ambiguous, external, or
recursive references. Do not follow links from a resolved contract or module.
Do not load an unselected contract or module.

Treat files under `references/modules/` as private role dependencies. Reject a
module if it contains skill frontmatter, defines a direct command or workflow
transition, expands the role's write boundary, or attempts to load another
module. Modules may define only reusable artifact, shared, authoring, consuming,
or reviewing rules for the selected role.

Construct a compact role-run manifest containing the run ID, brief path,
applicable direct-link paths in their written order, declared project-input
paths and canonical hashes, explicit artifact paths and canonical hashes,
expected output path, allowed write path, and the exact receipt forms from the
execution contract. Pass skill resources by path only after validating their
location and structure; do not compute or persist their content hashes. Include
the ordered clarification history relevant to the role's declared inputs, plus
current findings or a persisted pending response when the transition requires
them. Let the executor read files directly; do not quote their bodies merely to
relay them. Instruct the role not to inspect parent chat or undeclared runtime
data. Do not load or reference any unrelated role brief, contract, or module.

Give a role a sandbox restricted to its one output when available. Only when an
exact write sandbox is unavailable, snapshot the project filesystem before the
role run, verify it after completion, and reject every unexpected change. A
reviewer requires write access to its exact review file and no other path.

When a role's selected rules permit a blocking question, accept only the valid
`blocked` JSON receipt defined by the execution contract and only if the role
made no file changes. Reject a raw question as a malformed receipt. Never
reinterpret a question as an artifact, finding correction, or role failure.

## Routing

### Initial idea dialog

For `new` without non-whitespace idea text, ask one direct question requesting
the idea and stop without reading project configuration or writing any file.
Treat only the user's immediate bare reply as the idea for that same `new`
transition. If the reply is empty or is itself a question instead of an idea,
answer when possible and ask again without writing. A later or ambiguous reply
requires a new explicit `feature new` invocation.

After state exists, a framer must return a valid `blocked` receipt containing
one question whenever the idea authoring evidence gate fails. Persist its
question in `pending`, keep `request.md` unchanged, and present only the question
to the user. Treat the user's immediate bare reply as its answer; otherwise
require `answer <spec-id> <text>`. Persist the response before launching a fresh
framer with `request.md`, prior clarification history, and that response. If the
fresh framer returns another valid blocked receipt, first archive the answered
pending item in `clarifications`, then replace it with the new pending question
and continue the dialog. Do not create `idea.md` until the framer can satisfy the
complete authoring evidence gate without another material product decision.

For `new`:

1. Resolve execution configuration under the execution contract and stop on any
   invalid or unavailable explicit binding.
2. Derive one `id_hint` from the complete initial idea under the identifier
   rules, then generate `spec-id` and collision data with `scripts/stepan.py`.
   Use an uncollided result without announcing the hint or calculation. On
   collision, present only the choice between resuming `spec_id` and creating
   `next_available` under the interactive-choice rules before writing.
3. Create the directory, immutable `request.md`, and initial `state.yaml` with
   the request hash and resolved execution snapshot. If this initialization
   cannot complete, remove only files created by this attempt and stop.
4. Dispatch a fresh framer through its bound adapter, passing `request.md` by
   path and canonical hash.

For an existing change, read state and verify all recorded hashes before acting.
If `active_run` is non-null, reconcile that exact run before applying any normal
status transition: inspect or wait for the existing host subagent when
available, or inspect the matching mailbox response. When an interrupted host
run cannot be inspected, preserve it and require an explicit decision to
abandon and retry. Never dispatch a second run while one is active. Otherwise
apply the single matching transition:

- `drafting`: dispatch the current stage owner;
- `waiting-executor`: inspect the active mailbox run; accept its response or
  leave the same run pending without redispatching;
- owner returns a valid `blocked` receipt: set `awaiting-decision`, store its
  question in `pending` with `kind: clarification` and `origin: author`, and
  stop;
- successful `idea` author: set `awaiting-approval`;
- successful later author: set `reviewing` and dispatch a fresh reviewer;
- review `pass`: set `awaiting-approval`;
- review contains a blocking `resolution: user-decision` finding: set
  `awaiting-decision`, store its direct question in `pending` with
  `kind: clarification` and `origin: review`, and stop without automatic
  revision;
- repairable `changes-required` whose blocking findings all use
  `resolution: author-revision`: increment `automatic_revision_attempts`, set
  `revising`, dispatch a fresh owner with findings, then review again;
- third unsuccessful automatic revision: set `awaiting-decision`, store one
  direct next-step question in `pending` with `kind: revision` and
  `origin: router`, and do not launch a fourth;
- `awaiting-approval` or `awaiting-decision`: present one user-facing checkpoint
  question and stop;
- `stage: plan`, `status: approved`: give one short completion outcome and stop.

Before each dispatch, invoke bundled `reserve-run` once against verified state;
do not edit `active_run` or `next_run_sequence` directly. Reread its atomic state
replacement, require it to match the returned reservation, and only then invoke
the bound adapter under the execution contract. Validate every executor response
with the bundled receipt validator. For a completed receipt, verify all declared
project-data hashes and the fallback project snapshot when one was taken before
applying the matching transition and clearing the run. For a blocked receipt,
require no file changes before persisting its one question. For an invalid
receipt, use at most one same-agent format repair when the selected native
adapter defines it; otherwise preserve the run and stop. For a valid failed
receipt or an invalid repaired receipt, preserve the run and stop.

When the user answers `pending` through an allowed immediate bare reply or an
explicit `answer` action, persist the answer before launching a fresh role. For
`origin: author`, relaunch that owner with the answer. For
`origin: review`, launch the current stage owner with the review and answer,
then review the resulting artifact again. For `origin: router`, treat the answer
as user-directed revision guidance and launch the current stage owner. Clear
`pending` only after the owner processes the answer successfully; archive an
answered clarification in `clarifications` before clearing or replacing it.
Keep an answered revision request out of `clarifications`. Reset the automatic
revision counter after a user-directed revision or stage transition.

If a later stage exposes a material upstream defect, return to that artifact's
owner, remove approvals for it and all downstream artifacts, and preserve
downstream files as stale until their input hashes are reviewed again.

## User-facing communication

Treat the workflow's orchestration as private implementation detail, subject to
any progress, tool, or safety notice the host itself requires.

- Do not narrate resource loading, configuration resolution, stages, roles,
  subagents, dispatches, state values, hashes, run IDs, snapshots, receipts,
  verification, or automatic revision attempts. Never relay a role receipt to
  the user.
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
  artifact location needed for later use. Do not recap the execution history.
- On failure, state the user impact and recovery action concisely. Include an
  internal identifier or technical detail only when it is necessary to recover
  safely or identify affected data. Ask one direct question when a user decision
  can unblock the workflow; do not invent a question for a terminal failure.
- For `status`, summarize product artifacts already prepared, the decision or
  work currently pending, and the next user action. Do not dump `state.yaml` or
  executor details unless the user explicitly requests diagnostics.
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

- `continue`: approve the current artifact and advance;
- `continue-and-commit`: approve and create the limited checkpoint commit;
- `revise <feedback>`: reset the automatic revision counter and dispatch a fresh
  owner, followed by review where applicable;
- `question <text>`: answer without changing artifacts or state;
- `answer <text>`: supply the answer to the one persisted pending question;
- `accept-risk <finding-ids> [comment]`: record explicit acceptance of an
  objective risk and advance;
- `stop`: make no further transition.

Normalize a terminal menu selection or an immediate fallback reply to exactly
one of these transitions before applying its existing validation rules. Never
interpret a category selection, silence, an ambiguous label, or a request for
more information as approval.

Allow `continue` only for `idea` or a `pass` review. Require `revise` or explicit
`accept-risk` for blocking `author-revision` findings. Never allow
`accept-risk`, `continue`, or automatic revision to bypass a `user-decision`
finding or unanswered `pending` question. Silence never approves.

Allow `answer` only when `pending` is non-null. An immediate bare answer and an
explicit `answer` command have the same transition semantics; never interpret
either as approval. Keep `question` read-only and distinct from `answer`.

Record each approval under `approvals.<stage>` with `artifact_sha256`, the
applicable `review_sha256`, and `accepted_risks`. After plan approval, keep
`stage: plan` and set `status: approved`.

On `continue-and-commit`, commit only `.stepan/specs/<spec-id>/` with message
`stepan(<spec-id>): approve <stage>`. Do not stage unrelated changes. Do not advance
until the commit succeeds and its contents are verified.

## Identifiers and hashes

Treat JSON returned by `scripts/stepan.py` as the sole source of truth for
generated identifiers, run reservations, canonical hashes, and validated
receipts. Do not reproduce, adjust, or second-guess its computations.

Use canonical hashes for project data only: the immutable request, generated
artifacts and reviews, declared project inputs, and persisted configuration when
the execution contract requires it. Validate briefs, contracts, modules, and
bundled scripts as skill resources by canonical path, existence, readability,
and applicable structure rules; do not hash them to pin a content version for a
role run.

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

- On a role failure or interruption, preserve durable state and stop. A later
  explicit resume must inspect any persisted active run before launching a fresh
  role. An invalid native response may receive only the selected adapter's one
  same-agent format repair before this stop rule applies.
- On a mailbox timeout, remain at `waiting-executor`, preserve the request and
  active run, and stop without treating ordinary waiting as failure.
- On a late response for an abandoned, completed, or unknown run, do not apply
  it or modify repository state. Mention it only when it affects the requested
  action, and keep run details for explicit diagnostics.
- On an unexpected write, project-data hash mismatch, malformed file, unknown
  schema, or manual change to an approved artifact, do not accept, overwrite,
  reset, or commit. When state can be updated safely, set `awaiting-decision`, persist one
  direct question with `kind: clarification` and `origin: router`, and present
  only the affected product data and recovery choice needed to answer it. Keep
  the exact technical discrepancy for explicit diagnostics unless it is needed
  to identify affected data safely.
- On commit failure, remain at the same checkpoint. Remove only router-created
  temporary/index changes; never roll back user- or hook-created files.
- Ask one blocking question at a time. Never turn uncertainty into approval.
