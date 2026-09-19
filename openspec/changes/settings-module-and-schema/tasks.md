## 1. Settings boundary

- [x] 1.1 Create `internal/setting` as the sole loader for both full settings documents, reject old top-level sections with source paths, and verify missing sections and malformed documents in package tests.
- [x] 1.2 Implement typed effective profiles, role and limit precedence, profile deletion, and existing defaults in `setting`; verify merge and role resolution tests.
- [x] 1.3 Move loop checks, required checks, rules file and main branch validation into `setting`, enforce project-only fields, and verify the existing check and rules tests.

## 2. Secret boundary

- [x] 2.1 Read the Nessy token only from the home document through a dedicated `setting` callback, reject project token configuration, and verify absent, invalid, whitespace and environment override cases.
- [x] 2.2 Ensure Nessy child diagnostics redact the configured token and effective settings and bootstrap context never contain it; verify focused provider and bootstrap tests.

## 3. Consumers and bootstrap

- [x] 3.1 Switch CLI, loop start, resume and runtime composition to `setting`, preserving accepted configuration during a run and reloading at resume; verify start and resume tests.
- [x] 3.2 Update bootstrap proposal response, validation and per-file diff to the new section hierarchy; preserve unrelated JSON and the home token, and verify confirmation and rejection tests.

## 4. Documentation and verification

- [x] 4.1 Update settings examples, user guidance and architecture import rules for the new hierarchy; verify the documentation example and architecture tests.
- [x] 4.2 Run `go build -o stepan.exe ./cmd/stepan`, `go test ./...`, applicable integration suites and OpenSpec validation; fix any failures and verify all commands pass.
