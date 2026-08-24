# Project initialization protocol

## Command surface

Accept exactly these target actions:

- `$stepan init codex` in Codex;
- `$stepan init claude` in Codex;
- `/stepan init claude` in Claude Code.

Accept no additional arguments. Keep `codex` unavailable from Claude Code. On
an omitted or unknown target, show only the targets available on the current
host and stop before resolving the project root, inspecting a destination, or
writing anything.

The explicit command authorizes creation of only the recommended project-local
Stepan configuration declared for its target below. It does not authorize
overwriting, merging, or deleting an existing file, changing user-level host
configuration, installing a model or tool, or modifying any other project file.

## Deterministic execution

Resolve the canonical project root from the current workspace and the canonical
skill root under the shared router rules. Require an already available `uv`
executable without installing `uv` or Python. For the `codex` target, run
exactly:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" init-codex --host codex --project-root "<project-root>"
```

For the `claude` target, substitute the actual current host in the declared
placeholder and run exactly:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" init-claude --host <codex|claude-code> --project-root "<project-root>"
```

Keep both isolation flags and their order. Never fall back to `python`,
`python3`, or `py`. `--no-project` prevents this initialization command from
discovering or synchronizing the project's Python environment, and
`--no-python-downloads` prevents an implicit interpreter download.

Do not recreate generated content in a prompt or shell command. Before
preflight, the script generates and parses every host document and
`.stepan/config.yaml`, and verifies their exact paths, names, models, effort,
profile names, role bindings, and named router binding. Only after those checks
does it preflight every destination. It leaves byte-identical files unchanged,
creates missing files with UTF-8 and LF line endings, and stops before writing
when any destination differs, is a symlink, or is not a regular file beneath
ordinary project directories. It never follows a destination symlink and has
no force mode. During an ordinary error it removes only files and empty
directories created by that invocation.

For `codex`, the script may create only:

```text
.codex/agents/stepan_orchestrator.toml
.codex/agents/stepan_author.toml
.codex/agents/stepan_architect.toml
.codex/agents/stepan_planner.toml
.codex/agents/stepan_reviewer.toml
.stepan/config.yaml
```

For `claude`, the script may create only:

```text
.claude/agents/stepan-orchestrator.md
.claude/agents/stepan-author.md
.claude/agents/stepan-architect.md
.claude/agents/stepan-planner.md
.claude/agents/stepan-reviewer.md
.claude/settings.json
.claude/hooks/stepan-runtime.py
.stepan/config.yaml
```

Accept only a zero exit status and one JSON object containing exactly
`schema_version: 1`, `host`, `status`, `created`, and `unchanged`. Require
`host: codex` for the `codex` target and `host: claude-code` for the `claude`
target. Require `status` to be `created` when `created` is non-empty and
`unchanged` otherwise. Require `created` and `unchanged` to be disjoint arrays
whose union is exactly the selected target's declared paths. Treat malformed
output or any other filesystem effect as failure.

## Recommended Codex profile

The generated `.stepan/config.yaml` declares reusable project-scoped profiles
and maps the feature roles to them. Every generated profile contains the
explicit empty declaration `project_inputs: []`; the project owner may later
replace it with an ordered list of project-relative evidence files, after which
another init correctly reports a conflict. Model settings remain in the custom
agent TOML files:

| Profile | Agent name and filename stem | Model | Reasoning |
| --- | --- | --- | --- |
| orchestrator | `stepan_orchestrator` | `gpt-5.6` | `high` |
| author | `stepan_author` | `gpt-5.6` | `high` |
| architect | `stepan_architect` | `gpt-5.6` | `high` |
| planner | `stepan_planner` | `gpt-5.6-terra` | `high` |
| reviewer | `stepan_reviewer` | `gpt-5.6-terra` | `high` |

Use the stronger model for orchestration and ambiguous authoring, and Terra for
the narrower artifact-consuming planner and reviewer. Do not silently replace a
model or reasoning effort based on current availability. A later manual change
belongs to the project owner and makes a subsequent init report a conflict.
The feature router binding is always the non-null `orchestrator` profile, whose
agent is always the named `stepan_orchestrator`; initialization has no default,
primary-conversation, null-router, or weaker-model fallback.

Do not create `.codex/config.toml`: current Codex releases enable subagents by
default, and this workflow must not disturb unrelated project Codex settings.

## Recommended Claude Code profile

The generated `.claude/agents/*.md` files use Claude Code project custom-agent
frontmatter. The generated `.claude/settings.json` selects
`stepan-orchestrator` as the named main-thread agent and installs a
`SessionStart` check backed by `.claude/hooks/stepan-runtime.py`. Claude Code
subagents cannot launch the fresh role subagents required by the feature
workflow, so the router must be the main thread. These settings take effect for
a newly started project session; init does not claim to transform the current
session.

The generated `.stepan/config.yaml` uses the concrete `claude-code` adapter,
binds the named main-thread router, and keeps every `project_inputs` list
explicitly empty:

| Profile | Agent name and filename stem | Model family | Effort |
| --- | --- | --- | --- |
| orchestrator | `stepan-orchestrator` | `opus` | `high` |
| author | `stepan-author` | `opus` | `high` |
| architect | `stepan-architect` | `opus` | `high` |
| planner | `stepan-planner` | `sonnet` | `high` |
| reviewer | `stepan-reviewer` | `sonnet` | `high` |

Use host aliases so Claude Code resolves the recommended model version for the
active provider. Treat a reported concrete model as matching an alias only when
it belongs to that model family; an older provider-specific version may still
match `opus` or `sonnet`. Never accept another family as a fallback.

At Claude session start and every feature launch, require the current main
thread to report agent name `stepan-orchestrator`, an `opus` model, and `high`
effort before resolving configuration or changing feature state. The generated
hook stops the session with a configuration-mismatch error when a value differs
or the host does not expose it. The workflow repeats the check so `--agent`,
`--model`, `/model`, `--effort`, `/effort`, environment, resume, and
organization-policy overrides cannot silently reconfigure the router after
project initialization.

Before every Claude role dispatch, require its project agent definition to
retain the generated model family and effort. Reject a non-empty
`CLAUDE_CODE_SUBAGENT_MODEL` other than `inherit`, because it overrides every
role agent's project model, and reject a global effective effort other than
`high`. When the host exposes an effective role model or effort, require it to
match before accepting the role result. If the host cannot expose a role's
effective model, rely only on the validated definition plus absence of a global
model override; do not claim that the provider's final routing was observed.

Do not merge `.claude/settings.json`. If it already contains unrelated project
settings, init reports a conflict and leaves every destination unchanged; the
project owner may manually add `"agent": "stepan-orchestrator"` and reconcile
the generated files.

## User-facing result

On creation, report that the selected project-local profiles and workflow
bindings are configured and name `.stepan/config.yaml` as the binding
entrypoint. For Claude, also state that a new Claude Code project session is
required before running a feature workflow. When every file was already
identical, report that the selected host configuration was already initialized.
On an unavailable target for the current host, report the valid target or
targets and state that no project files were inspected or changed.
On conflict, state that no settings were changed and list only the conflicting
paths so the user can choose whether to keep or manually reconcile them. Do not
dump generated prompts, model internals, or the script's JSON result unless the
user explicitly asks for diagnostics.
