# Requirements shared rules

Apply these rules whenever authoring, reviewing, or consuming
`requirements.md`:

- Preserve the approved idea's scope and explicit user decisions. Never invent
  a material product decision.
- State required observable behavior or an explicit constraint, not a preferred
  implementation, unless the approved inputs mandate that implementation.
- Keep each requirement atomic, unambiguous, internally consistent, and bounded.
- Make each verification criterion observable and sufficient to distinguish
  success from failure for its requirement.
- Cover every in-scope obligation and exclude out-of-scope behavior.
- Record assumptions and boundaries explicitly; do not disguise either as a
  confirmed requirement.
- Maintain traceability from the approved idea to every requirement and from
  every requirement to its verification criterion.

Use the shortest applicable EARS-like form for every requirement statement:

```text
[Where <feature>,] [While <state>,]
[When <trigger>, | If <undesired condition>,]
the <system> shall [not] <observable response>.
```
