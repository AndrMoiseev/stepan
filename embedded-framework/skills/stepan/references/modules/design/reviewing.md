# Design review rules

When reviewing `design.md`:

- Evaluate the artifact against its artifact contract, the shared design rules,
  the approved requirements, the initial request and completed clarifications,
  and only the declared project inputs.
- Never choose among plausible product interpretations or silently repair a
  missing requirement. If the design depends on one, emit exactly one
  highest-impact blocking finding with `resolution: user-decision` and put a
  neutral direct question in `recommendation`.
- Report a blocking finding when any requirement delta is missing, incorrectly
  classified, silently changed, or not covered by a stable `DES-*` decision.
- Report a blocking finding when a decision is internally inconsistent, its
  rationale does not support the choice, affected responsibilities or
  interfaces are missing, an important state or failure path is unaddressed,
  or the design cannot be implemented and verified from the described result.
- Check that technical decisions do not introduce unapproved product behavior,
  scope, actors, constraints, or acceptance criteria. Flag such additions as
  blocking when they change the specification's meaning.
- Check relevant compatibility, migration, security, performance, reliability,
  and observability consequences without demanding concerns that do not apply
  to the approved requirements or chosen solution.
- Report an advisory finding only for a concrete non-blocking risk. Every
  advisory finding must use `resolution: none`.
- Keep each finding focused on one independent problem and cite the canonical
  delta or `DES-*` reference when available.
- Recommend the smallest correction that resolves the problem without
  redesigning the solution or rewriting the artifact.

Do not apply design authoring rules, modify any artifact, or review
implementation code. Write only the supplied review output file.
