# `design.md` contract

Keep every required section non-empty. Additional useful sections are allowed.

```text
# Design
## Overview
## Decisions
### DES-001 — <short name>
Covers: ADDED Requirement "<exact requirement name>", ...
Decision: <chosen solution>
Rationale: <why>
## Affected components
## Constraints, risks and trade-offs
## Verification
```

Cover every delta entry with at least one stable `DES-*`. Use these exact
traceability forms:

- `ADDED Requirement "<name>"`
- `MODIFIED Requirement "<name>"`
- `REMOVED Requirement "<name>"`
- `RENAMED Requirement "<old name>" -> "<new name>"`

For a delta with no implementation impact, record and justify that conclusion in
its covering decision. Never renumber an existing design decision ID.
