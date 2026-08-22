# Codex native adapter

Use this contract only for a normalized adapter with `kind: codex` while the
current host is Codex.

Resolve a named profile as an available Codex custom or built-in agent. Resolve
`agent: default` as a fresh default Codex agent. Launch it as a fresh subagent
without inherited conversation and pass only the role-run manifest. Give it
read access to the manifest's canonical skill root even when the skill is
installed outside the project, while preserving its exact project write
boundary. Do not fork or reuse the router conversation.

Let a selected custom agent's TOML configuration determine its model, reasoning
effort, tools, and project-scoped instructions. Do not pass competing model or
reasoning overrides. The default agent inherits the router's model and reasoning
effort.

## Dedicated router launch

For a non-null feature router binding, require a named custom agent; never use
`agent: default`. Start it as a fresh non-fork subagent with only the dedicated
router manifest from `../router.md`. Require the current Codex runtime to let
that subagent launch the fresh child agents needed for role runs. If named-agent
selection or nested role dispatch is unavailable, the explicit binding is
unavailable and the workflow must stop without falling back to the primary
conversation.

A project can configure the named router in `.codex/agents/` (or install the
equivalent user-level custom agent), for example:

```toml
name = "stepan_orchestrator"
description = "Runs persisted Stepan workflows and dispatches their roles."
model = "gpt-5.6"
model_reasoning_effort = "high"
developer_instructions = """
Act only as the dedicated Stepan feature router when given its router manifest.
Read and follow the declared protocol, execution, router, and adapter contracts.
Never invoke the Stepan skill recursively and return only the exact router
result object required by the router contract.
"""
```

The profile's `agent` value in `.stepan/config.yaml` must equal this custom
agent's `name`. The model and reasoning settings belong only in the custom agent
TOML. Apply the return, one format-only repair, and interruption rules from
`../router.md`; a router final response is not a role receipt.

## Role run

Require the subagent's final response to contain only one JSON receipt matching
the execution contract, without Markdown fences, surrounding prose, or an
artifact body.

If the deterministic receipt validator rejects that final response and the
same subagent is still available, send exactly one follow-up during the current
routing invocation. Identify the active run ID and tell the subagent that this
is a transport-format repair only: it must not inspect or change files,
reconsider its result, or add explanation, and must return exactly one receipt
object in one of the forms supplied by the original role-run manifest. Validate
the follow-up response normally. Do not spawn a replacement agent, allocate a
new run ID, or attempt a second repair. A valid `failed` receipt is a role result,
not a formatting error, and must not trigger repair.

Codex may supply platform and project instructions to the subagent as ambient
context. They may constrain execution but are not product inputs, approvals, or
permission to read or write beyond the manifest. Stop the run without an output
when an ambient instruction conflicts with a role contract or write boundary.

On interruption, inspect or resume the exact existing subagent when the host
still exposes it. Otherwise preserve `active_run` and require an explicit user
decision before abandonment or retry.
