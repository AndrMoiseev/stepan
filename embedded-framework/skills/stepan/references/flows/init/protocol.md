# Project initialization protocol

## Command surface

Select this workflow only from an explicit `$stepan init codex` request in
Codex. Accept no additional arguments. On another current host, reject the
command before resolving the project root, inspecting a destination, or writing
anything. On an omitted or unknown target, show only `codex` and stop without
reading project configuration or writing.

The explicit command authorizes creation of the recommended project-local
Stepan configuration listed below. It does not authorize overwriting, merging,
or deleting an existing file, changing user-level Codex configuration,
installing a model or tool, or modifying any other project file.

## Deterministic execution

Resolve the canonical project root from the current workspace and the canonical
skill root under the shared router rules. Require an already available `uv`
executable without installing `uv` or Python. Run exactly:

```text
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" init-codex --host codex --project-root "<project-root>"
```

Keep both isolation flags and their order. Never fall back to `python`,
`python3`, or `py`. `--no-project` prevents this initialization command from
discovering or synchronizing the project's Python environment, and
`--no-python-downloads` prevents an implicit interpreter download.

Do not recreate generated content in a prompt or shell command. The script may
inspect only its exact destinations and may create only:

```text
.codex/agents/stepan_orchestrator.toml
.codex/agents/stepan_author.toml
.codex/agents/stepan_architect.toml
.codex/agents/stepan_planner.toml
.codex/agents/stepan_reviewer.toml
.stepan/config.yaml
```

Before preflight, the script requires the explicit current-host value `codex`.
It then generates and parses every TOML document and the mapping-only YAML
configuration, and verifies their exact filenames, agent names, models, profile
names, role bindings, and named router binding. Only after those checks does it
preflight every destination. It leaves byte-identical files unchanged, creates
missing files with UTF-8 and LF line endings, and stops before writing when any
destination differs, is a symlink, or is not a regular file beneath ordinary
project directories. It never follows a destination symlink and has no force
mode. During an ordinary error it removes only files and empty directories
created by that invocation.

Accept only a zero exit status and one JSON object containing exactly
`schema_version: 1`, `host: codex`, `status`, `created`, and `unchanged`. Require
`status` to be `created` when `created` is non-empty and `unchanged` otherwise.
Require `created` and `unchanged` to be disjoint arrays whose union is exactly
the six declared paths. Treat malformed output or any other filesystem effect
as failure.

## Recommended Codex profile

The generated `.stepan/config.yaml` declares reusable project-scoped profiles
and maps the feature roles to them. Model settings remain in the custom agent
TOML files:

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

## User-facing result

On creation, report that the project-local profiles and workflow bindings are
configured and name `.stepan/config.yaml` as the binding entrypoint. When every
file was already identical, report that configuration was already initialized.
On another current host, report that initialization is Codex-only and that no
project files were inspected or changed.
On conflict, state that no settings were changed and list only the conflicting
paths so the user can choose whether to keep or manually reconcile them. Do not
dump generated prompts, model internals, or the script's JSON result unless the
user explicitly asks for diagnostics.
