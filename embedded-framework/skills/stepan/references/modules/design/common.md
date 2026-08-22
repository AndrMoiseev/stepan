# Design shared rules

Apply these rules whenever authoring, reviewing, or consuming `design.md`:

- derive the solution from approved requirements; do not silently add, remove,
  rename, or reinterpret product behavior;
- make every requirement delta traceable to at least one design decision, or
  explicitly record why it has no implementation impact;
- prefer the smallest feasible solution and state meaningful alternatives when
  a trade-off affects scope, risk, cost, schedule, or operability;
- define affected components, responsibilities, interfaces, data/control flows,
  and important failure or recovery behavior at the level needed to implement
  and verify the change;
- keep product decisions in requirements and technical decisions in design;
  raise a blocking question when the design depends on an unresolved product
  decision;
- identify constraints, assumptions confirmed by approved inputs, risks, and
  mitigations without turning guesses into facts;
- give each material decision a concrete verification method and preserve the
  link from verification back to the covered requirement or design decision;
- do not include implementation code, incidental file-by-file instructions, or
  unrelated refactoring.
