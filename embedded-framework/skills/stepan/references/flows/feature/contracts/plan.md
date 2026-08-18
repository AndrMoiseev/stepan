# `plan.md` contract

Keep every required section non-empty. Additional useful sections are allowed.

```text
# Plan
## Steps
### STEP-001 — <short name>
Covers: ADDED Requirement "<exact requirement name>", DES-001, ...
Outcome: <one verifiable result>
Changes: <expected components or paths>
Verification: <command or observable check>
## Final verification
```

Keep steps ordered. Cover every delta entry using the traceability forms defined
by the `design.md` contract, and cover every `DES-*`, with at least one stable
`STEP-*`. A no-implementation-impact design decision still requires a
verification step. Never renumber an existing step ID.
