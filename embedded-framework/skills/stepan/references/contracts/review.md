# Review data contract

Return data matching this schema. Use the canonical input hashes supplied by the
router; do not compute or alter them.

```yaml
schema_version: 1
stage: design
inputs:
  idea.md: sha256:...
  requirements.md: sha256:...
  design.md: sha256:...
verdict: pass | changes-required
findings:
  - id: DES-R-001
    severity: blocking | advisory
    references: [DES-003, REQ-007]
    problem: "..."
    recommendation: "..."
```

Accept only `schema_version: 1`. A `pass` verdict may contain advisory findings
but no blocking finding. Preserve the IDs of unresolved findings across reviews;
never renumber an existing finding ID.
