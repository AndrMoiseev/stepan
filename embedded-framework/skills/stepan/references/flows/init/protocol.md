# Project initialization protocol

## Command surface

Select this workflow only from an explicit `$stepan init codex` request in
Codex or `/stepan init codex` request in Claude Code. Accept no additional
arguments. On an omitted or unknown target, show only `codex` and stop without
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
uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" init-codex --project-root "<project-root>"
```

Keep both isolation flags and their order. Never fall back to `python`,
`python3`, or `py`. `--no-project` prevents this initialization command from
discovering or synchronizing the project's Python environment, and
`--no-python-downloads` prevents an implicit interpreter download.

Do not recreate generated content in a prompt or shell command. The script may
inspect only its exact destinations and may create only:

```text
.codex/agents/stepan_feature_router.toml
.codex/agents/stepan_feature_framer.toml
.codex/agents/stepan_feature_specifier.toml
.codex/agents/stepan_feature_designer.toml
.codex/agents/stepan_feature_planner.toml
.codex/agents/stepan_feature_reviewer.toml
.stepan/config.yaml
```

The script preflights every destination before writing. It leaves byte-identical
files unchanged, creates missing files with UTF-8 and LF line endings, and stops
before writing when any destination differs, is a symlink, or is not a regular
file beneath ordinary project directories. It never follows a destination
symlink and has no force mode. During an ordinary error it removes only files
and empty directories created by that invocation.

Accept only a zero exit status and one JSON object containing exactly
`schema_version: 1`, `host: codex`, `status`, `created`, and `unchanged`. Require
`status` to be `created` when `created` is non-empty and `unchanged` otherwise.
Require `created` and `unchanged` to be disjoint arrays whose union is exactly
the seven declared paths. Treat malformed output or any other filesystem effect
as failure.

## Recommended Codex profile

The generated `.stepan/config.yaml` binds the dedicated router and all five
feature roles to project-scoped named Codex agents. Model settings remain in the
custom agent TOML files:

| Agent | Model | Reasoning |
| --- | --- | --- |
| router | `gpt-5.6` | `high` |
| framer | `gpt-5.6` | `high` |
| specifier | `gpt-5.6` | `high` |
| designer | `gpt-5.6` | `high` |
| planner | `gpt-5.6-terra` | `high` |
| reviewer | `gpt-5.6-terra` | `high` |

Use the stronger model for orchestration and ambiguous authoring, and Terra for
the narrower artifact-consuming planner and reviewer. Do not silently replace a
model or reasoning effort based on current availability. A later manual change
belongs to the project owner and makes a subsequent init report a conflict.

Do not create `.codex/config.toml`: current Codex releases enable subagents by
default, and this workflow must not disturb unrelated project Codex settings.

## User-facing result

On creation, report that the project-local router and five role agents are
configured and name `.stepan/config.yaml` as the binding entrypoint. When every
file was already identical, report that configuration was already initialized.
On conflict, state that no settings were changed and list only the conflicting
paths so the user can choose whether to keep or manually reconcile them. Do not
dump generated prompts, model internals, or the script's JSON result unless the
user explicitly asks for diagnostics.
