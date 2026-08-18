# Requirements shared rules

Apply these rules whenever authoring, reviewing, or consuming
`requirements.md`:

- Preserve the approved idea's scope and explicit user decisions. Treat only
  explicit user statements and approved declared inputs as product decisions.
- Treat every unresolved uncertainty about intended behavior, scope,
  constraints, priorities, acceptance criteria, baseline meaning, or delta
  classification as a blocking need for user clarification. Never resolve it
  by selecting a default, common convention, likely intent, or preferred
  interpretation. When unsure whether a doubt can affect meaning, ask the user.
- Treat the specification as a behavior contract, not an implementation plan.
  State observable behavior or an explicit external constraint, not preferred
  libraries, classes, functions, schemas, or execution steps.
- Keep each `SHALL` statement in an `ADDED` or `MODIFIED` requirement atomic,
  unambiguous, internally consistent, and bounded.
- Give every `ADDED` or `MODIFIED` requirement at least one concrete, testable
  scenario that exercises its `SHALL` statement rather than merely restating it.
- Cover important success, edge, and error cases in proportion to product risk.
- Cover every in-scope obligation and exclude out-of-scope behavior.
- Record only assumptions and boundaries explicitly confirmed by the user or an
  approved declared input. Never turn an inference into an assumption to avoid
  asking a question.
- Maintain traceability from the approved idea and applicable baseline to every
  delta entry. Identify an entry by its delta operation and exact requirement
  name; for a rename, preserve both the exact source and target names.
- Treat `REMOVED` and `RENAMED` as first-class specification changes even though
  they do not contain behavioral scenarios.
