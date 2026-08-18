---
name: stepan
description: Route repository-scoped Stepan workflows when the user invokes $stepan, asks to choose a Stepan workflow, or explicitly requests a supported command such as `$stepan feature new`. Use the `feature` workflow for pre-development feature specifications. Do not use for ordinary planning, design, review, or coding requests that do not explicitly request Stepan.
---

# Stepan workflow router

## Select a workflow

Parse invocations as `$stepan <workflow> <action> [arguments]`.

| Workflow | Purpose | Primary command | Contract |
| --- | --- | --- | --- |
| `feature` | Specify a feature before implementation | `$stepan feature new [idea]` | [`references/flows/feature/protocol.md`](references/flows/feature/protocol.md) |

- On `$stepan` without a workflow, show the supported workflows and their
  primary commands, then stop without writing.
- On `$stepan <workflow>` without an action, show that workflow's actions from
  its protocol, then stop without writing.
- On an unknown workflow or action, show only the valid choices and stop without
  writing.
- Never infer an omitted workflow from repository contents or chat history.

## Dispatch the selected workflow

1. Read only the selected workflow's protocol completely and treat it as the
   normative routing contract.
2. Require the protocol to define the requested action unambiguously before
   reading workflow-specific roles or contracts or writing repository state.
3. Load only the roles, contracts, scripts, and repository inputs selected by
   that protocol. Do not load resources belonging to another workflow.
4. Execute only the deterministic transitions allowed by the selected protocol.
5. Stop safely on a missing or ambiguous workflow resource, invalid state,
   unavailable required capability, or unexpected repository change.

## Preserve router boundaries

- Keep workflow selection in this file and workflow behavior under
  `references/flows/<workflow>/`.
- Add a workflow only by adding one explicit table entry and one self-contained
  protocol directory.
- Do not let one workflow read or mutate another workflow's state unless both
  protocols explicitly define that interaction.
