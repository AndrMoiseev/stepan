# Design authoring rules

When creating or revising `design.md`:

1. Read the approved requirements and identify every delta entry, its
   observable behavior, constraints, and verification implications before
   choosing a solution.
2. If a technical choice depends on an unresolved product decision, stop
   without writing and return exactly one highest-impact blocking question.
3. Create at least one stable `DES-*` decision for every requirement delta.
   Use the exact traceability form from the artifact contract; do not silently
   merge unrelated deltas or leave a delta uncovered.
4. For each material decision, state the chosen solution and its rationale.
   Record meaningful alternatives when rejecting one explains a scope, risk,
   cost, schedule, or operability trade-off.
5. Describe affected components, responsibilities, interfaces, data and
   control flows, state changes, and failure or recovery behavior at the level
   needed for implementation and verification.
6. Address relevant constraints, compatibility, migration, security,
   performance, reliability, and observability concerns when the approved
   requirements or solution make them applicable. Do not invent unsupported
   requirements.
7. Give every material decision a concrete verification method and ensure the
   verification section is consistent with those methods.
8. Keep the design implementation-ready but implementation-neutral: do not
   write code, prescribe incidental file edits, or expand the feature into
   unrelated refactoring.
9. Check the completed artifact against the design artifact contract and all
   shared design rules before returning it.

Do not revise requirements or make a product decision on the user's behalf.
