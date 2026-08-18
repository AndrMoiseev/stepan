---
name: stepan
description: Run the repository-scoped Stepan pre-development flow when the user explicitly asks to start, continue, inspect, revise, approve, or stop a Stepan specification, or invokes $stepan. Do not use for ordinary planning, design, review, or coding requests that do not explicitly request the Stepan flow.
---

# Stepan Router

## Load the contract

1. Read `references/protocol.md` completely.
2. Treat that bundled reference as the normative routing contract. Do not load
   a repository-specific Stepan document or require one to exist.
3. Before validating role-owned data or launching a role, read its matching
   brief under `references/roles`; do not load unrelated briefs.
4. From each selected brief's `Contracts` section, resolve only the direct links
   that apply to the current stage. Load exactly those files under
   `references/contracts` and treat them with the brief as the role execution
   contract. Do not follow links from a contract.
5. Stop without writing if the protocol is missing, does not define one
   unambiguous transition, or a required brief or contract is missing,
   ambiguous, outside its allowed directory, or recursively linked.

## Route the flow

1. Resolve the explicit user action: start, resume, status, checkpoint action, or
   stop.
2. Treat `.stepan/specs/<spec-id>/` and the repository as the source of truth;
   do not rely on chat history.
3. Validate the current state, approval hashes, expected files, and fresh-agent
   capability before launching a role.
4. Use `scripts/stepan.py` for spec identifiers and canonical hashes; never
   reimplement those algorithms in the router.
5. Execute only the deterministic transitions allowed by the protocol. Continue
   automatic role and review work only until the next user checkpoint or blocker.
6. Launch every substantive role through its project-scoped custom agent with no
   inherited conversation. Build its prompt only from the matching role brief,
   contracts directly declared by that brief for the current stage, declared
   inputs and their canonical hashes when reviewing, expected output, write
   boundary, and current feedback or pending response when applicable. Do not
   perform a role in the router context.
7. Verify the actual write boundary after every role. Accept no result that
   changed an unexpected path.
8. Modify `state.yaml` and persist reviewer output only as router. Never rewrite
   an approved artifact silently.
9. At a checkpoint, show the artifact, current review verdict and findings, then
   offer only the actions valid for that state.

## Preserve boundaries

- Require an explicit Stepan request; never infer one from an ordinary task.
- Do not implement code. End at `stage: plan`, `status: approved`.
- Keep the portable protocol, role prompts, schemas, and deterministic scripts in
  this skill. Use project-scoped custom agents for project inputs, model,
  reasoning effort, and sandbox configuration.
- Do not commit unless the user explicitly selects `continue-and-commit` at the
  current checkpoint.
- On ambiguity, unexpected repository changes, failed validation, or unavailable
  fresh-agent capability, stop safely and report the single blocking condition.
