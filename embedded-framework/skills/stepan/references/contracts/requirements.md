# `requirements.md` contract

Keep every required section non-empty. Additional useful sections are allowed.

```text
# Requirements
## Requirements
### REQ-001 — <short name>
Statement: <one atomic requirement>
Verification: <observable check>
## Boundaries and assumptions
```

Use the shortest applicable EARS-like form:

```text
[Where <feature>,] [While <state>,]
[When <trigger>, | If <undesired condition>,]
the <system> shall [not] <observable response>.
```

Split independent obligations into separate stable `REQ-*` IDs. Never renumber
an existing requirement ID.
