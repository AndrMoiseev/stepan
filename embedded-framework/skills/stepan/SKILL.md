---
name: stepan
description: Route repository-scoped Stepan workflows only when the user explicitly invokes `$stepan` in Codex or `/stepan` in Claude Code. Use the `feature` workflow for pre-development feature specifications. Do not invoke for an ordinary planning, design, review, or coding request.
---

# Stepan workflow router

## Select a workflow

Accept either host command token and normalize it away before parsing:

- Codex: `$stepan <workflow> <action> [arguments]`;
- Claude Code: `/stepan <workflow> <action> [arguments]`.

Require an explicit user invocation through the current host to select and
start a workflow. After selection, accept an immediate bare reply only when the
selected workflow protocol explicitly allows it for a question or checkpoint
the router just presented. Never treat any other mention, quoted example, agent
instruction, or implicit skill selection as a Stepan command.

When the router or selected protocol requires a finite user choice, use a
host-native structured selection capability if it is callable in the current
mode. Otherwise use the protocol's compact natural-language fallback. Never
change host mode, add a tool, or launch a separate UI merely to obtain a menu.

| Workflow | Purpose | Primary command | Contract |
| --- | --- | --- | --- |
| `feature` | Specify a feature before implementation | `$stepan feature new [idea]` or `/stepan feature new [idea]` | [`references/flows/feature/protocol.md`](references/flows/feature/protocol.md) |

- On an invocation without a workflow, show the supported workflows and their
  primary commands, then stop without writing.
- On an invocation with a workflow but no action, show that workflow's actions from
  its protocol, then stop without writing.
- On an unknown workflow or action, show only the valid choices and stop without
  writing.
- Never infer an omitted workflow from repository contents or chat history.

## Dispatch the selected workflow

1. Read only the selected workflow's protocol completely and treat it as the
   normative routing contract.
2. Require the protocol to define the requested action unambiguously before
   reading workflow-specific roles or contracts or writing repository state.
3. Load only the roles, contracts, private modules, scripts, and repository
   inputs selected by that protocol. Do not load resources belonging to another
   workflow.
4. Execute only the deterministic transitions allowed by the selected protocol.
5. Stop safely on a missing or ambiguous workflow resource, invalid state,
   unavailable required capability, or unexpected repository change.
6. Treat routing, resource loading, role dispatch, state transitions, and
   verification as private mechanics. Surface only the user-facing interaction
   required by the selected protocol and any notice the host itself requires.

## Preserve router boundaries

- Keep workflow selection in this file, workflow behavior under
  `references/flows/<workflow>/`, and reusable role knowledge under
  `references/modules/`.
- Treat modules as private role dependencies, not workflows or skills. Give
  modules no `SKILL.md`, agent metadata, direct command, or implicit loading.
- Load a module only when the selected workflow protocol and role brief declare
  it directly.
- Add a workflow only by adding one explicit table entry, one protocol
  directory, and any directly declared reusable modules.
- Do not let one workflow read or mutate another workflow's state unless both
  protocols explicitly define that interaction.
