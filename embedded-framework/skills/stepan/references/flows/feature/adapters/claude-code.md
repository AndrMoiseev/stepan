# Claude Code native adapter

Use this contract only for a normalized adapter with `kind: claude-code` while
the current host is Claude Code.

Resolve a named role executor as an available Claude Code custom or built-in
subagent. Project custom agents live under `.claude/agents/`; user agents may be
available from the user's Claude Code configuration. Resolve `agent: default`
as the built-in `general-purpose` subagent. Launch a fresh non-fork role
subagent and pass only the role-run manifest. Give it read access to the
manifest's canonical skill root even when the skill is installed outside the
project, while preserving its exact project write boundary.

Let the selected agent's Markdown frontmatter determine its model, effort,
tools, and project-scoped instructions. Do not pass per-invocation model or
effort overrides. The default agent inherits the router's model and effort.

## Dedicated router launch

Claude Code subagents cannot launch subagents, so never start the feature router
through the Agent tool. For the non-null router binding, require the current
main thread itself to have been started as that named project custom agent
through the project `agent` setting or `--agent`; never accept `agent: default`
or an ordinary main session as a fallback.

Before resolving feature configuration, reading feature state, or writing, read
the selected `.claude/agents/<agent>.md` definition and require its exact `name`,
`model`, and `effort` fields. Require the host's current runtime identity to
report the same agent name, a concrete model in the configured family, and the
same effective effort. Repeat this check at every explicit feature launch and
immediate continuation so a resumed session or `/model` or `/effort` change
cannot silently reconfigure the router. If any value differs or the runtime
does not expose it, stop with a concise configuration-mismatch error and make no
workflow change.

The recommended project router definition is:

```markdown
---
name: stepan-orchestrator
description: "Orchestrates persisted Stepan workflows and dispatches role runs."
model: opus
effort: high
---

Act only as the named Stepan workflow orchestrator for this project.
Before any workflow transition, require the current Claude Code agent name, model family, and effort to match this project agent definition; stop on a mismatch or unavailable runtime value.
Read and follow every protocol and every execution, router, or adapter contract selected by the explicit Stepan invocation.
Never invoke the Stepan skill recursively or treat unrelated conversation history as product input.
Dispatch only the fresh role runs selected by persisted workflow state.
```

Keep model and effort only in the selected custom agent's Markdown frontmatter,
not `.stepan/config.yaml`. Treat unrelated conversation history as undeclared
ambient context, never as product input. Execute the dedicated router
responsibilities from `../router.md` in this verified main thread. Construct and
validate its router result normally, but present only its validated `message`
instead of returning the raw result to a separate launcher.

Apply the return, one format-only repair, and interruption rules from
`../router.md`; a router final response is not a role receipt.

## Role run

Require the subagent's final response to contain only one JSON receipt matching
the execution contract, without Markdown fences, surrounding prose, or an
artifact body.

Before dispatch, require the named project agent definition to retain the model
family and effort selected by project initialization. Reject a non-empty
`CLAUDE_CODE_SUBAGENT_MODEL` other than `inherit`, because it overrides the
project definition for every role. Also reject a global effective effort that
differs from the selected definition. When Claude Code exposes an effective
role model or effort, require it to match before accepting the receipt. When it
does not expose an effective role model, validate the project definition and
absence of the global model override but do not claim to have observed the
provider's final model routing.

Run the generated preflight from the canonical project root immediately before
reserving and dispatching the role, substituting only the persisted agent name
and that project agent definition's validated model family and effort:

```text
uv run --no-project --no-python-downloads "<project-root>/.claude/hooks/stepan-runtime.py" role-preflight --project-root "<project-root>" --agent <agent> --model-family <opus|sonnet> --effort <effort>
```

Accept only zero exit status and one JSON object containing exactly `agent`,
`model_family`, `effort`, and `status: verified`, all matching the selected
project definition. On any other result, report a configuration mismatch and
stop before creating `active_run`.

Claude Code supplies `CLAUDE.md` instructions and a git-status snapshot to
custom and general-purpose subagents. Treat them only as ambient host context:
they may constrain execution but are not product inputs, approvals, or
permission to inspect undeclared files or write beyond the manifest. Stop the
run without an output when ambient instructions conflict with the role brief, a
declared resource, or the write boundary. When a project requires an executor
with no ambient project context, bind that role to the mailbox adapter instead.

On interruption, inspect or resume the exact existing role subagent when Claude Code
still exposes its agent ID. Otherwise preserve `active_run` and require an
explicit user decision before abandonment or retry.
