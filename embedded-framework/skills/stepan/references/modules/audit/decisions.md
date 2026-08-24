# Decision reporting

Use the final completed receipt to report the role's material decisions. The
`decisions` array is a current result snapshot, not a transcript of the work or
a delta from an earlier run.

## Material decisions

Report a choice only when it materially affects product behavior, scope,
architecture, compatibility, risk, cost, schedule, implementation ordering, or
verification. Do not report incidental wording, formatting, trivial naming, or
an option that was not genuinely considered.

State `summary` as the selected result. State `rationale` as a concise,
result-level explanation based on relevant evidence, constraints, or trade-offs.
Neither field may contain private reasoning traces or a step-by-step account of
how the result was reached.

When a material alternative was considered, record the option and the specific
reason it was rejected. Leave `alternatives` empty when no material alternative
was evaluated; do not invent alternatives to populate the list.

## Snapshot and authority

For `authority: agent`, report the complete current set of material decisions
owned by this role for the completed output. On revision, include retained and
revised decisions as they now stand, omit retired decisions, and do not return
only what changed during the run.

Use `authority: user` only to normalize an accepted user statement from declared
user-input evidence. Preserve its meaning without broadening it, and link every
such normalization through at least one supplied `source_event_keys` value. Use
`authority: agent` for a material choice owned by the role and keep
`source_event_keys` empty.

## References and evidence

Use `references` for the exact canonical artifact or review identifiers that
the decision describes. Reference only identifiers present in the role's
validated inputs or completed output, following the role brief's coverage
rules; leave `references` empty when that contract has no stable identifier for
the decision. Keep `references` and `source_event_keys` ordered and unique.
Never invent a source event key; use only keys supplied as declared user-input
evidence.

Do not read, search, or cite `mem-log.md`. The audit log is not a role input or
a source of current product truth.

## Response and write boundary

Return decisions only in the final `completed` receipt. Do not stream them or
write a separate decision or audit file. The assigned artifact or review stays
the role's sole permitted filesystem write. `blocked` and `failed` receipts
remain minimal and do not contain `decisions`.
