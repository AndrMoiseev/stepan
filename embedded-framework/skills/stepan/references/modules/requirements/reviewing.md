# Requirements review rules

When reviewing `requirements.md`:

- Evaluate the artifact against its artifact contract, the shared requirements
  rules, the approved idea, and only the declared project inputs.
- Report a blocking finding for a missing in-scope obligation, conflicting or
  invented behavior, a non-atomic material obligation, an ambiguous statement,
  an unverifiable requirement, or a broken idea-to-requirement trace.
- Report an advisory finding only for a concrete non-blocking risk. Ignore
  stylistic preferences that do not affect correctness or verification.
- Keep each finding focused on one independent problem and cite the relevant
  `REQ-*` ID or source section when available.
- Recommend the smallest correction that resolves the problem without designing
  the solution or rewriting the artifact.

Do not apply requirements authoring rules or modify any file.
