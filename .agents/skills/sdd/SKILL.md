---
name: sdd
description: Run the repository-scoped SDD pre-development flow when the user explicitly asks to start, continue, inspect, revise, approve, or stop an SDD specification, or invokes $sdd. Do not use for ordinary planning, design, review, or coding requests that do not explicitly request the SDD flow.
---

# SDD Router

## Load the contract

1. Read `references/protocol.md` completely.
2. Treat that bundled reference as the complete normative contract. Do not load
   a repository-specific SDD document or require one to exist.
3. Stop without writing if the reference is missing or does not define one
   unambiguous transition.

## Route the flow

1. Resolve the explicit user action: start, resume, status, checkpoint action, or
   stop.
2. Treat `.stepan/specs/<spec-id>/` and the repository as the source of truth;
   do not rely on chat history.
3. Validate the current state, approval hashes, expected files, and fresh-agent
   capability before launching a role.
4. Use `scripts/sdd.py` for spec identifiers and canonical hashes; never
   reimplement those algorithms in the router.
5. Execute only the deterministic transitions allowed by the protocol. Continue
   automatic role and review work only until the next user checkpoint or blocker.
6. Launch every substantive role through its project-scoped custom agent with no
   inherited conversation and only its role brief, declared inputs, expected
   output, and write boundary. Do not perform a role in the router context.
7. Verify the actual write boundary after every role. Accept no result that
   changed an unexpected path.
8. Modify `state.yaml` and persist reviewer output only as router. Never rewrite
   an approved artifact silently.
9. At a checkpoint, show the artifact, current review verdict and findings, then
   offer only the actions valid for that state.

## Preserve boundaries

- Require an explicit SDD request; never infer one from an ordinary task.
- Do not implement code. End at `stage: plan`, `status: approved`.
- Keep the portable protocol, role prompts, schemas, and deterministic scripts in
  this skill. Use project-scoped custom agents for project inputs, model,
  reasoning effort, and sandbox configuration.
- Do not commit unless the user explicitly selects `continue-and-commit` at the
  current checkpoint.
- On ambiguity, unexpected repository changes, failed validation, or unavailable
  fresh-agent capability, stop safely and report the single blocking condition.
