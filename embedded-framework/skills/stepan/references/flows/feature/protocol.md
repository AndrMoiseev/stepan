# Feature specification protocol

## Contents

- [Invariants](#invariants)
- [Command surface](#command-surface)
- [Tooling](#tooling)
- [Storage and schemas](#storage-and-schemas)
- [Role dispatch](#role-dispatch)
- [Routing](#routing)
- [Checkpoints](#checkpoints)
- [Identifiers and hashes](#identifiers-and-hashes)
- [Failure handling](#failure-handling)

## Invariants

- Run only after the Stepan router selects the `feature` workflow from an
  explicit `$stepan feature <action>` request.
- Route `idea → requirements → design → plan`; stop after plan approval.
- Treat repository files, not chat history, as the source of truth.
- Launch every author and every review as a fresh agent without inherited
  conversation. Pass only declared files and feedback.
- Let authors write only their own artifact. Let only the router write
  `state.yaml` and `review/*.yaml`.
- Never silently change an approved artifact or infer a material user decision.
- Never implement code, run a code-review flow, push, merge, or deploy.

Keep project inputs and their discovery rules in each role's project-scoped
custom agent. Do not prescribe repository-wide project documents here. Load only
the inputs declared for the role being run.

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
python .agents/skills/stepan/scripts/stepan.py hash <file>...
python .agents/skills/stepan/scripts/stepan.py self-test
```

Stop without writing if the script is unavailable, fails, or returns malformed
JSON. Do not reproduce its algorithms in a prompt or shell one-liner.

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
schema_version: 1
created_at: 2026-08-15T12:00:00Z
stage: idea
status: drafting
automatic_revision_attempts: 0
approvals: {}
pending: null
```

Allow these values:

- `stage`: `idea`, `requirements`, `design`, `plan`;
- `status`: `drafting`, `reviewing`, `revising`, `awaiting-approval`,
  `awaiting-decision`, `approved`.

Store one unresolved question or revision request as:

```yaml
pending:
  kind: clarification | revision
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
| reviewer | `roles/reviewer.md` | current review artifacts; previous review when needed | review data | none |

Resolve each selected brief's `Contracts` section before validation or launch.
Load only direct Markdown links that apply to the current stage, require every
resolved path to be under this workflow's `contracts/` directory, and reject
missing, ambiguous, external, or recursive contract references. Do not load an
unselected contract.

Construct the role prompt from the brief, its resolved contracts, explicit input
paths and their canonical hashes when reviewing, expected output path, and
allowed write path. Include current findings or a persisted pending response
only when the transition requires them. Instruct the role not to inspect parent
chat or undeclared runtime data. Do not load or quote any unrelated role brief.

Use a read-only sandbox for the reviewer when available. Always snapshot the
repository before a review and reject the result if the reviewer changed any
file.

## Routing

For `new`:

1. Generate `spec-id` and collision data with `scripts/stepan.py`, show the result,
   and resolve a collision before writing.
2. Create the directory and initial `state.yaml`.
3. Launch a fresh framer.

For an existing change, read state and verify all recorded hashes before acting.
Then apply the single matching transition:

- `drafting`: launch the current stage owner;
- successful `idea` author: set `awaiting-approval`;
- successful later author: set `reviewing` and launch a fresh reviewer;
- review `pass`: set `awaiting-approval`;
- repairable `changes-required`: increment `automatic_revision_attempts`, set
  `revising`, launch a fresh owner with findings, then review again;
- finding requiring user choice: set `awaiting-decision` and persist it in
  `pending`;
- third unsuccessful automatic revision: set `awaiting-decision`; do not launch
  a fourth;
- `awaiting-approval` or `awaiting-decision`: show the checkpoint and stop;
- `stage: plan`, `status: approved`: report ready for implementation and stop.

When the user answers `pending`, persist the answer before launching a fresh
role. Clear `pending` only after that role processes it successfully. Reset the
automatic revision counter after a user-directed revision or stage transition.

If a later stage exposes a material upstream defect, return to that artifact's
owner, remove approvals for it and all downstream artifacts, and preserve
downstream files as stale until their input hashes are reviewed again.

## Checkpoints

Offer only:

- `continue`: approve the current artifact and advance;
- `continue-and-commit`: approve and create the limited checkpoint commit;
- `revise <feedback>`: reset the automatic revision counter and launch a fresh
  owner, followed by review where applicable;
- `question <text>`: answer without changing artifacts or state;
- `accept-risk <finding-ids> [comment]`: record explicit acceptance and advance;
- `stop`: make no further transition.

Allow `continue` only for `idea` or a `pass` review. Require `revise` or explicit
`accept-risk` for blocking findings. Silence never approves.

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

## Failure handling

- On a role failure or interruption, preserve durable state and stop. A later
  explicit resume may launch a fresh role from saved files.
- On an unexpected write, hash mismatch, malformed file, unknown schema, or
  manual change to an approved artifact, do not accept, overwrite, reset, or
  commit. Set `awaiting-decision` when state can be updated safely and show the
  exact discrepancy.
- On commit failure, remain at the same checkpoint. Remove only router-created
  temporary/index changes; never roll back user- or hook-created files.
- Ask one blocking question at a time. Never turn uncertainty into approval.
