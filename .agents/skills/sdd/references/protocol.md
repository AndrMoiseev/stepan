# SDD pre-development protocol

## Contents

- [Invariants](#invariants)
- [Tooling](#tooling)
- [Storage and schemas](#storage-and-schemas)
- [Artifact contracts](#artifact-contracts)
- [Role briefs](#role-briefs)
- [Routing](#routing)
- [Checkpoints](#checkpoints)
- [Identifiers and hashes](#identifiers-and-hashes)
- [Failure handling](#failure-handling)

## Invariants

- Start or resume only after an explicit user request.
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

## Tooling

Use the bundled Python script for deterministic identifiers and hashes. It emits
JSON, uses only the Python 3 standard library, and never writes repository state:

```text
python .agents/skills/sdd/scripts/sdd.py spec-id --idea <text> --root .stepan/specs
python .agents/skills/sdd/scripts/sdd.py hash <file>...
python .agents/skills/sdd/scripts/sdd.py self-test
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

Write each review with this schema:

```yaml
schema_version: 1
stage: design
inputs:
  idea.md: sha256:...
  requirements.md: sha256:...
  design.md: sha256:...
verdict: pass | changes-required
findings:
  - id: DES-R-001
    severity: blocking | advisory
    references: [DES-003, REQ-007]
    problem: "..."
    recommendation: "..."
```

`pass` may contain advisory findings but no blocking finding. Accept only
`schema_version: 1`; stop without writing on any other version.

## Artifact contracts

Keep all required sections non-empty. Additional useful sections are allowed.

### `idea.md`

```text
# Idea
## Problem
## Goal
## Target user
## Expected outcome
## Scope
### In
### Out
```

### `requirements.md`

```text
# Requirements
## Requirements
### REQ-001 — <short name>
Statement: <one atomic requirement>
Verification: <observable check>
## Boundaries and assumptions
```

Use the shortest applicable EARS-like form:

```text
[Where <feature>,] [While <state>,]
[When <trigger>, | If <undesired condition>,]
the <system> shall [not] <observable response>.
```

Split independent obligations into separate stable `REQ-*` IDs.

### `design.md`

```text
# Design
## Overview
## Decisions
### DES-001 — <short name>
Covers: REQ-001, ...
Decision: <chosen solution>
Rationale: <why>
## Affected components
## Constraints, risks and trade-offs
## Verification
```

Cover every `REQ-*` with at least one stable `DES-*`.

### `plan.md`

```text
# Plan
## Steps
### STEP-001 — <short name>
Covers: REQ-001, DES-001, ...
Outcome: <one verifiable result>
Changes: <expected components or paths>
Verification: <command or observable check>
## Final verification
```

Keep steps ordered. Cover every `REQ-*` and `DES-*` with at least one stable
`STEP-*`.

## Role briefs

Construct each role prompt from the matching brief, explicit input paths,
expected output path, and allowed write path. Instruct the role not to inspect
parent chat or undeclared runtime data.

### Framer

Read the user's initial request and the project inputs declared by the framer's
project-scoped agent. Write only `idea.md` using its contract. If a material
decision is missing, write nothing and return one minimal blocking question.

### Specifier

Read approved `idea.md` and the project inputs declared by the specifier's
project-scoped agent. Write only `requirements.md`. Make requirements complete,
consistent, atomic, testable, bounded, and traceable. If a material decision is
missing, write nothing and return one minimal blocking question.

### Designer

Read approved `idea.md`, `requirements.md`, and the project inputs declared by
the designer's project-scoped agent. Write only `design.md`. Choose the smallest
feasible solution, cover every requirement, and state affected components,
constraints, risks, trade-offs, and verification. If a material decision is
missing, write nothing and return one minimal blocking question.

### Planner

Read all approved artifacts and the project inputs declared by the planner's
project-scoped agent. Write only `plan.md`. Do not redesign the solution. Produce
ordered, verifiable steps covering all requirements and decisions. If a material
decision is missing, write nothing and return one minimal blocking question.

### Spec reviewer

Read only the artifacts for the current review and the previous review when
preserving unresolved finding IDs. Do not write any file. Return review data
matching the review schema. Check correctness, completeness, consistency,
testability, scope, traceability, feasibility, risks, and unjustified complexity.
Ignore style preferences without correctness impact.

Use a read-only sandbox when available. Always snapshot the repository before
the review and reject the result if the reviewer changed any file.

## Routing

For a new change:

1. Generate `spec-id` and collision data with `scripts/sdd.py`, show the result,
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
`sdd(<spec-id>): approve <stage>`. Do not stage unrelated changes. Do not advance
until the commit succeeds and its contents are verified.

## Identifiers and hashes

The bundled `scripts/sdd.py` is the executable implementation of these rules.
Build `spec-id` by lowercasing the idea, transliterating Russian letters with
this fixed table, replacing remaining non-`[a-z0-9]` runs with `-`, trimming
hyphens, and limiting the result to 63 characters:

```text
а=a б=b в=v г=g д=d е=e ё=yo ж=zh з=z и=i й=y к=k л=l м=m н=n
о=o п=p р=r с=s т=t у=u ф=f х=kh ц=ts ч=ch ш=sh щ=shch ъ= ы=y
ь= э=e ю=yu я=ya
```

Use `change` if the result is empty. On collision, ask whether to resume the
existing change or create a new one with the smallest free suffix `-2`, `-3`,
and so on. Truncate the base as needed to keep the full ID within 63 characters.

Never renumber existing `REQ-*`, `DES-*`, `STEP-*`, or finding IDs.

Before hashing a Markdown or YAML file:

1. require valid UTF-8 and remove one leading BOM;
2. replace `CRLF` and lone `CR` with `LF`;
3. remove terminal `LF` characters and append exactly one `LF`;
4. preserve all other code points and whitespace without Unicode normalization.

Compute SHA-256 over the resulting UTF-8 bytes. Always obtain it from
`scripts/sdd.py hash`.

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
