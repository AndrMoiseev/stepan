Write a specification that includes:

- a link to `intent.md`;
- current and target behavior;
- requirements as Markdown headings with stable `REQ-*` IDs;
- architectural and cross-cutting decisions as headings with stable `DEC-*` IDs;
- applicable interfaces, data, errors, migrations, security, and compatibility;
- acceptance criteria as headings with stable `AC-*` IDs;
- verification approach, exclusions, and `Open questions`.

Use three-digit padding when issuing IDs. IDs are positive and permanent: preserve an ID for the same item, never issue zero, never use alternate padded spellings of one numeric identity in the same document, and never reuse a removed or rejected identity. Every `AC-*` has a `Traces:` field that references at least one existing `REQ-*`. Use the exact English field name `Traces:` regardless of document language.

The specification contains no implementation task decomposition. Leave `Open questions` empty when there are no explicitly deferred questions; never write `None`.
