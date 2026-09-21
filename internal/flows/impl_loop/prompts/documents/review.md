# Implementation review result format

Return the result through the flat JSON response transport, for both task review and final review.

For `review_passed`, message and references are non-empty. Explain the conclusion and identify its supporting evidence.

For `changes_requested`, every finding array is non-empty and finding_ids, findings, finding_decisions, finding_reasons, locations, bases, and expected_results have equal lengths. Items at the same index describe one finding:

- `finding_ids`: stable finding identity;
- `findings`: concrete problem;
- `locations`: affected location;
- `bases`: defect, explicit requirement, or project rule supporting the finding;
- `expected_results`: expected outcome of correction;
- `finding_decisions`: `open` for a new finding, `resolved` or `retained` for a prior finding;
- `finding_reasons`: concrete reviewer reason for the decision.

For each prior finding, use resolved or retained with a concrete reviewer reason; use open only for a new finding.
