# Claude Code native adapter

Use this contract only for a normalized adapter with `kind: claude-code` while
the current host is Claude Code.

Resolve a named executor as an available Claude Code custom or built-in
subagent. Project custom agents live under `.claude/agents/`; user agents may be
available from the user's Claude Code configuration. Resolve `agent: default`
as the built-in `general-purpose` subagent. Launch a fresh non-fork subagent and
pass only the role-run manifest. Never run the Stepan router itself with
`context: fork`, because the router must dispatch sequential role runs.

Let the selected agent's Markdown frontmatter determine its model, effort,
tools, and project-scoped instructions. Do not pass per-invocation model or
effort overrides. The default agent inherits the router's model and effort.

Claude Code supplies `CLAUDE.md` instructions and a git-status snapshot to
custom and general-purpose subagents. Treat them only as ambient host context:
they may constrain execution but are not product inputs, approvals, or
permission to inspect undeclared files or write beyond the manifest. Stop the
run without an output when ambient instructions conflict with a role contract
or write boundary. When a project requires an executor with no ambient project
context, bind that role to the mailbox adapter instead.

On interruption, inspect or resume the exact existing subagent when Claude Code
still exposes its agent ID. Otherwise preserve `active_run` and require an
explicit user decision before abandonment or retry.
