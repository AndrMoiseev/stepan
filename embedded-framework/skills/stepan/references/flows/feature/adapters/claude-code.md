# Claude Code native adapter

Use this contract only for a normalized adapter with `kind: claude-code` while
the current host is Claude Code.

Resolve a named executor as an available Claude Code custom or built-in
subagent. Project custom agents live under `.claude/agents/`; user agents may be
available from the user's Claude Code configuration. Resolve `agent: default`
as the built-in `general-purpose` subagent. Launch a fresh non-fork subagent and
pass only the role-run manifest. Give it read access to the manifest's canonical
skill root even when the skill is installed outside the project, while
preserving its exact project write boundary. Never run the Stepan router itself
with `context: fork`, because the router must dispatch sequential role runs.

Let the selected agent's Markdown frontmatter determine its model, effort,
tools, and project-scoped instructions. Do not pass per-invocation model or
effort overrides. The default agent inherits the router's model and effort.

## Dedicated router launch

For a non-null feature router binding, require a named custom agent; never use
`agent: default`. Start it as a fresh non-fork subagent with only the dedicated
router manifest from `../router.md`. Require the current Claude Code runtime to
let that subagent launch the fresh child agents needed for role runs. If named
agent selection or nested role dispatch is unavailable, the explicit binding is
unavailable and the workflow must stop without falling back to the primary
conversation. Keep its model and effort in the selected custom agent's Markdown
frontmatter, not `.stepan/config.yaml`.

Apply the return, one format-only repair, and interruption rules from
`../router.md`; a router final response is not a role receipt.

## Role run

Require the subagent's final response to contain only one JSON receipt matching
the execution contract, without Markdown fences, surrounding prose, or an
artifact body.

Claude Code supplies `CLAUDE.md` instructions and a git-status snapshot to
custom and general-purpose subagents. Treat them only as ambient host context:
they may constrain execution but are not product inputs, approvals, or
permission to inspect undeclared files or write beyond the manifest. Stop the
run without an output when ambient instructions conflict with the role brief, a
declared resource, or the write boundary. When a project requires an executor
with no ambient project context, bind that role to the mailbox adapter instead.

On interruption, inspect or resume the exact existing subagent when Claude Code
still exposes its agent ID. Otherwise preserve `active_run` and require an
explicit user decision before abandonment or retry.
