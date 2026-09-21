# Implementation prompt assets

Each role's stable instructions live in `roles/`; `common.md` applies to every
role. The flow embeds these files and appends response transport instructions
generated from the role's allowed actions.

The controller builds current run and assignment context in Go. The flat
response transport schema is embedded from `../response_schema.json`; Go adds
the allowed `kind` values for each role and validates response semantics.
