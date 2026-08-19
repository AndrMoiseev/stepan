# Review data contract

Write data matching this schema only to the supplied review output path. Use the
canonical input hashes supplied by the router; do not compute or alter them.

```yaml
schema_version: 2
stage: design
inputs:
  idea.md: sha256:...
  requirements.md: sha256:...
  design.md: sha256:...
verdict: pass | changes-required
findings:
  - id: DES-R-001
    severity: blocking | advisory
    resolution: author-revision | user-decision | none
    references: [DES-003, 'MODIFIED Requirement "Session Expiration"']
    problem: "..."
    recommendation: "..."
```

Accept only `schema_version: 2`. Require every finding to declare `resolution`.
Use `user-decision` only for a blocking uncertainty that requires the user's
answer. Use `author-revision` only when approved inputs determine the correction
without a new product decision. Use `none` only for an advisory finding. A
`pass` verdict may contain advisory findings but no blocking finding or
`user-decision`. Preserve the IDs of unresolved findings across reviews; never
renumber an existing finding ID. Require `stage` to match the reviewed stage and
`inputs` to contain exactly the canonical hashes supplied by the router. Use
stage-specific finding IDs: `REQ-R-*` for requirements, `DES-R-*` for design,
and `PLAN-R-*` for plan.
