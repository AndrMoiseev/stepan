# Review data contract

Write data matching this schema only to the supplied review output path. Use the
canonical input hashes supplied by the router; do not compute or alter them.

```yaml
schema_version: 2
stage: requirements
inputs:
  - path: docs/changes/specs/export-data/request.md
    sha256: sha256:...
  - path: docs/changes/specs/export-data/idea.md
    sha256: sha256:...
  - path: docs/changes/specs/export-data/requirements.md
    sha256: sha256:...
verdict: pass | changes-required
findings:
  - id: REQ-R-001
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
`inputs` to be an ordered list containing exactly the project-relative paths and
canonical hashes supplied in the role manifest, including the immutable
request, applicable artifacts, and selected profile project inputs. The
clarification history is manifest data rather than a file hash. On re-review,
use the previous review feedback to preserve every finding ID whose problem
remains unresolved. For requirements review, include request, idea,
requirements, and declared project inputs; for design review, include request,
idea, requirements, design, and declared project inputs; for plan review, include
request, idea, requirements, design, plan, and declared project inputs. Use
stage-specific finding IDs: `REQ-R-*` for requirements, `DES-R-*` for design,
and `PLAN-R-*` for plan.
