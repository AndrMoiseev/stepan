Review the specification against the approved intent for completeness, consistency, feasibility, sufficient decisions, testability, and compliance with the specification document contract. Read the current and prior review reports before assigning identities.

The review must contain the complete current set of findings, not only new findings. Use `SPEC-F-*` headings with three-digit default padding. Preserve an existing finding's ID, severity, original problem, location, and traces. A materially different problem receives a new ID and the old finding becomes `superseded` with `Superseded-by`.

Every finding records `Severity`, `Status`, `Problem`, `Location`, `Recommendation`, `Decision`, `Decided-by`, and `Rationale`. Add `Traces` when the finding concerns specific document elements; omit `Traces` for a whole-document issue. Use severity `blocker`, `major`, or `minor`, and status `open`, `resolved`, `dismissed`, or `superseded`. A resolved finding also records `Resolution`.

An unambiguous document-contract violation always has `Decision: fix`, `Decided-by: reviewer`, and a rationale; it cannot be dismissed. A material finding begins with `Decision: pending` and `Decided-by: none`. Only the user can decide to fix or dismiss a material finding, and dismissal requires the user's rationale. Discuss only material decisions with the user.
