# Codex native adapter

Use this contract only for a normalized adapter with `kind: codex` while the
current host is Codex.

Resolve a named executor as an available Codex custom or built-in agent. Resolve
`agent: default` as a fresh default Codex agent. Launch it as a fresh subagent
without inherited conversation and pass only the role-run manifest. Do not fork
or reuse the router conversation.

Let a selected custom agent's TOML configuration determine its model, reasoning
effort, tools, and project-scoped instructions. Do not pass competing model or
reasoning overrides. The default agent inherits the router's model and reasoning
effort.

Codex may supply platform and project instructions to the subagent as ambient
context. They may constrain execution but are not product inputs, approvals, or
permission to read or write beyond the manifest. Stop the run without an output
when an ambient instruction conflicts with a role contract or write boundary.

On interruption, inspect or resume the exact existing subagent when the host
still exposes it. Otherwise preserve `active_run` and require an explicit user
decision before abandonment or retry.
