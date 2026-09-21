# Implementation prompt assets

Each role's stable instructions live in `roles/`; `common.md` applies to every
role. Document and result formats live separately in `documents/`:

- `brief.md`: assignment brief body and controller-owned document metadata;
- `review.md`: task and final review results and finding fields.

The flow embeds these files and includes the applicable format in each briefer
or reviewer prompt. It appends response transport instructions generated from
the role's allowed actions.

The controller builds current run and assignment context in Go. The flat
response transport schema is embedded from `../response_schema.json`; Go adds
the allowed `kind` values for each role and validates response semantics.
Changing a format's machine contract also requires updating its Go validation
and, when fields change, the transport schema.
