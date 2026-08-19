# Feature specification protocol

## Contents

- [Invariants](#invariants)
- [Command surface](#command-surface)
- [Tooling](#tooling)
- [Execution](#execution)
- [Storage and schemas](#storage-and-schemas)
- [Role dispatch](#role-dispatch)
- [Routing](#routing)
- [Checkpoints](#checkpoints)
- [Identifiers and hashes](#identifiers-and-hashes)
- [Failure handling](#failure-handling)

## Invariants

- Select this workflow only from an explicit `$stepan feature <action>` request.
  After selection, accept a bare checkpoint response only when the router just
  offered that response for the loaded specification.
- Route `idea → requirements → design → plan`; stop after plan approval.
- Treat repository files, not chat history, as the source of truth.
- Dispatch every author and every review as a fresh role run without inherited
  conversation. Pass only declared paths, hashes, and feedback.
- Let each role write only its exact declared output. Let only the router write
  `state.yaml`. Keep artifact bodies out of router context when a path and hash
  are sufficient.
- Never silently change an approved artifact or infer a product decision. At the
  requirements stage, treat every unresolved semantic uncertainty as requiring
  a user question before authoring, revision, review completion, or approval.
- Never implement code, run a code-review flow, push, merge, or deploy.

Keep project inputs and their discovery rules in each role's project-scoped
executor configuration. For a Codex executor, keep those rules in its custom
agent. Do not prescribe repository-wide project documents here. Load only the
inputs declared for the role being run.

## Command surface

Accept these actions after `$stepan feature`:

- `new [idea]`: create a feature specification; when the idea is omitted, ask
  for it before writing;
- `resume <spec-id>`: continue the single transition allowed by saved state;
- `status <spec-id>`: show saved state and the current checkpoint without
  changing it;
- `continue <spec-id>` and `continue-and-commit <spec-id>`: apply the matching
  checkpoint action;
- `revise <spec-id> <feedback>`, `question <spec-id> <text>`, and
  `accept-risk <spec-id> <finding-ids> [comment]`: apply the matching checkpoint
  action;
- `stop <spec-id>`: make no further transition.

Reject an omitted or unknown action without reading feature state or writing.

## Tooling

Use the bundled Python script for deterministic identifiers and hashes. It emits
JSON, uses only the Python 3 standard library, and never writes repository state:

```text
python .agents/skills/stepan/scripts/stepan.py spec-id --idea <text> --root .stepan/specs
python .agents/skills/stepan/scripts/stepan.py run-id --spec-id <id> --stage <stage> --role <role> --sequence <n>
python .agents/skills/stepan/scripts/stepan.py hash <file>...
python .agents/skills/stepan/scripts/stepan.py self-test
```

Stop without writing if the script is unavailable, fails, or returns malformed
JSON. Do not reproduce its algorithms in a prompt or shell one-liner.

## Execution

Read [`execution.md`](execution.md) completely before resolving project
configuration, creating feature state, or dispatching a role. Treat it as the
normative contract for project bindings, Codex and mailbox adapters, role-run
receipts, bounded waiting, and filesystem verification.

If `.stepan/config.yaml` is absent, use the built-in Codex binding defined by
the execution contract. If it exists, resolve it before writing and persist the
normalized execution snapshot in feature state. Never reread project execution
configuration to rebind an existing specification.

## Storage and schemas

Keep one change under:

```text
.stepan/specs/<spec-id>/
├── state.yaml
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
schema_version: 2
created_at: 2026-08-15T12:00:00Z
stage: idea
status: drafting
automatic_revision_attempts: 0
next_run_sequence: 1
approvals: {}
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
    framer: native-default
    specifier: native-default
    designer: native-default
    planner: native-default
    reviewer: native-default
```

Allow these values:

- `stage`: `idea`, `requirements`, `design`, `plan`;
- `status`: `drafting`, `reviewing`, `revising`, `awaiting-approval`,
  `awaiting-decision`, `waiting-executor`, `approved`.

Require `schema_version: 2`, a positive `next_run_sequence`, a normalized
`execution` snapshot matching the execution contract, and either `null` or one
valid `active_run`. Permit `waiting-executor` only with an active mailbox run.
Persist an active run before dispatch and increment `next_run_sequence` only
after reserving that run ID.

Store one unresolved question or revision request as:

```yaml
pending:
  kind: clarification | revision
  origin: author | review | router
  stage: design
  request: "..."
  response: null
```

## Role dispatch

Use the matching brief both to validate role-owned data and to launch a role:

| Role | Brief | Artifact inputs | Result | Allowed write |
| --- | --- | --- | --- | --- |
| framer | `roles/framer.md` | initial request | `idea.md` | expected `idea.md` only |
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

Construct a compact role-run manifest containing the brief path, applicable
direct-link paths in their written order, declared project-input paths, explicit
artifact paths and canonical hashes, expected output path, and allowed write
path. Include current findings or a persisted pending response only when the
transition requires them. Let the executor read those files directly; do not
quote their bodies merely to relay them. Instruct the role not to inspect parent
chat or undeclared runtime data. Do not load or reference any unrelated role
brief, contract, or module.

Snapshot the repository before every role run. Give a role a sandbox restricted
to its one output when available; otherwise verify the snapshot after completion
and reject every unexpected change. A reviewer requires write access to its
exact review file and no other path.

When a role's selected rules permit a blocking question, accept exactly one
direct question instead of its normal result only if it made no file changes.
Never reinterpret a question as an artifact, finding correction, or role
failure.

## Routing

For `new`:

1. Resolve execution configuration under the execution contract and stop on any
   invalid or unavailable explicit binding.
2. Generate `spec-id` and collision data with `scripts/stepan.py`, show the result,
   and resolve a collision before writing.
3. Create the directory and initial `state.yaml` with the resolved execution
   snapshot.
4. Dispatch a fresh framer through its bound adapter.

For an existing change, read state and verify all recorded hashes before acting.
If `active_run` is non-null, reconcile that exact run before applying any normal
status transition: inspect or wait for the existing Codex thread when available,
or inspect the matching mailbox response. When an interrupted Codex run cannot
be inspected, preserve it and require an explicit decision to abandon and retry.
Never dispatch a second run while one is active. Otherwise apply the single
matching transition:

- `drafting`: dispatch the current stage owner;
- `waiting-executor`: inspect the active mailbox run; accept its response or
  report that the same run is still pending without redispatching;
- owner returns a valid blocking question: set `awaiting-decision`, store it in
  `pending` with `kind: clarification` and `origin: author`, and stop;
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
- `awaiting-approval` or `awaiting-decision`: show the checkpoint and stop;
- `stage: plan`, `status: approved`: report ready for implementation and stop.

Before each dispatch, reserve and persist `active_run`, then invoke the bound
adapter under the execution contract. For a completed receipt, verify hashes and
the repository snapshot before applying the matching transition and clearing the
run. For a blocked receipt, require no file changes before persisting its one
question. For a failed or malformed receipt, preserve the run and stop.

When the user answers `pending`, persist the answer before launching a fresh
role. For `origin: author`, relaunch that owner with the answer. For
`origin: review`, launch the current stage owner with the review and answer,
then review the resulting artifact again. For `origin: router`, treat the answer
as user-directed revision guidance and launch the current stage owner. Clear
`pending` only after the owner processes the answer successfully. Reset the
automatic revision counter after a user-directed revision or stage transition.

If a later stage exposes a material upstream defect, return to that artifact's
owner, remove approvals for it and all downstream artifacts, and preserve
downstream files as stale until their input hashes are reviewed again.

## Checkpoints

Offer only:

- `continue`: approve the current artifact and advance;
- `continue-and-commit`: approve and create the limited checkpoint commit;
- `revise <feedback>`: reset the automatic revision counter and dispatch a fresh
  owner, followed by review where applicable;
- `question <text>`: answer without changing artifacts or state;
- `accept-risk <finding-ids> [comment]`: record explicit acceptance of an
  objective risk and advance;
- `stop`: make no further transition.

Allow `continue` only for `idea` or a `pass` review. Require `revise` or explicit
`accept-risk` for blocking `author-revision` findings. Never allow
`accept-risk`, `continue`, or automatic revision to bypass a `user-decision`
finding or unanswered `pending` question. Silence never approves.

Record each approval under `approvals.<stage>` with `artifact_sha256`, the
applicable `review_sha256`, and `accepted_risks`. After plan approval, keep
`stage: plan` and set `status: approved`.

On `continue-and-commit`, commit only `.stepan/specs/<spec-id>/` with message
`stepan(<spec-id>): approve <stage>`. Do not stage unrelated changes. Do not advance
until the commit succeeds and its contents are verified.

## Identifiers and hashes

Treat JSON returned by `scripts/stepan.py` as the sole source of truth for generated
identifiers and canonical hashes. Do not reproduce, adjust, or second-guess its
computations.

Accept `spec-id` output only when it contains non-empty string fields `spec_id`
and `next_available` plus a boolean `collision`. When `collision` is false, use
`spec_id`. When it is true, ask whether to resume `spec_id` or create
`next_available`; do not invent another identifier.

Accept `hash` output only when its `files` array contains exactly one entry for
every requested file. Require each entry to contain a string `path` and a
`sha256` value formatted as `sha256:` followed by 64 lowercase hexadecimal
characters. Record and compare the returned digest unchanged.

Accept `run-id` output only when it contains exactly one non-empty string field
`run_id` equal to `<spec-id>--<stage>--<role>--<sequence>`. Require the supplied
specification ID, stage, role, and positive sequence to match saved state. Never
invent, edit, or reuse a run ID.

## Failure handling

- On a role failure or interruption, preserve durable state and stop. A later
  explicit resume must inspect any persisted active run before launching a fresh
  role.
- On a mailbox timeout, remain at `waiting-executor`, preserve the request and
  active run, and stop without treating ordinary waiting as failure.
- On a late response for an abandoned, completed, or unknown run, do not apply
  it or modify repository state; report the stale response.
- On an unexpected write, hash mismatch, malformed file, unknown schema, or
  manual change to an approved artifact, do not accept, overwrite, reset, or
  commit. When state can be updated safely, set `awaiting-decision`, persist one
  direct question with `kind: clarification` and `origin: router`, and show the
  exact discrepancy.
- On commit failure, remain at the same checkpoint. Remove only router-created
  temporary/index changes; never roll back user- or hook-created files.
- Ask one blocking question at a time. Never turn uncertainty into approval.
