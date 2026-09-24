## Context

See [proposal.md](proposal.md). `implementationconfig` currently owns the loop's merge, defaults, checks and rules validation, while `usersettings` reads Nessy credentials. The loop, bootstrap and CLI each touch configuration, and bootstrap rewrites a top-level `implementation` member. The public JSON hierarchy and ownership must change together without putting a token into a run snapshot.

## Goals / Non-Goals

**Goals:** Give consumers one typed settings boundary, retain the existing loop's effective defaults and validation, and keep credential access separate from serializable values.

**Non-Goals:** Migrate old settings automatically, change the spec flow's CLI provider selection, or persist secrets with run state.

## Decisions

1. `internal/setting` owns source loading, schema validation, merging, profile resolution, loop limits, checks, rules and main branch resolution. The effective configuration is a value copied at start and reloaded only at resume. Existing pure validation algorithms move into this package so callers do not reimplement them. Alternative: keep `implementationconfig` behind a facade; this would leave two packages owning the same contract.
2. Parse each full settings document once into raw members. Reject old top-level keys in either document with a path-specific error. Extract `agentruntime.profiles` and `flows.impl_loop` independently, allowing absent flow sections. Reject project-only loop fields in the user document and `agentruntime.nessyapp.auth_token` in the project document. Keep unrelated future sections intact when writing. Alternative: decode one rigid root struct; it would couple unrelated flows and discard unknown sections.
3. Merge profiles by name with full replacement and project null deletion. Merge role and limit maps by key. Validate profile shape when a profile is resolved, and validate role references before starting the role. Preserve the loop's existing defaults and command/rules validators. Alternative: recursively merge profile objects; this would make a project override inherit unspecified user fields contrary to the spec.
4. Expose Nessy token through a dedicated `setting.NessyAuthToken` callback that rereads only the home document at the selected Nessy process boundary. Keep it out of `Configuration`, bootstrap context, diffs and run snapshots. The Nessy adapter's existing child environment override and output redaction remain in force. Alternative: include it in the effective configuration; serialization and diagnostics would then have to defend a much larger surface.
5. Bootstrap proposals are separate nonsecret user and project section values. Apply them to copies of complete documents, validate the resulting documents with `setting`, render redacted per-file diffs, and write after the existing single confirmation. Alternative: replace whole settings files; that would erase unrelated keys and the existing home token.
6. Change architecture rules and documentation at the same time as consumer imports. Keep compatibility solely in tests or internal transition code if needed during the edit, then remove the old production loading paths.

## Risks / Trade-offs

- [Existing configuration files stop working] → Return explicit file and legacy-key errors; document the manual schema update.
- [A token leaks through bootstrap or a Nessy diagnostic] → Never include token in effective values or proposal data; redact the configured value at the provider diagnostic boundary and test both paths.
- [Moving validators changes loop behavior] → Run the existing focused and full Go suites, with tests for merge, resume, checks and bootstrap.
- [Two files cannot be committed atomically by the filesystem] → Validate both before confirmation and retain each file's atomic replacement; report write failures without claiming both files changed together.

## Migration Plan

1. Introduce the new reader and typed configuration under `internal/setting`, with the new JSON paths and explicit old-key rejection.
2. Move callers, bootstrap proposal handling and Nessy callback to that contract.
3. Update examples, architecture rules and tests. Existing users manually change their settings files from `implementation`/`nessy` to the new hierarchy before running this version. A rollback requires restoring the prior binary and prior settings format.
