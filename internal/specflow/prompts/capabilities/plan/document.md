Write tasks as Markdown headings with stable `TASK-*` IDs and three-digit default padding. Every task has exactly one testable outcome and includes:

- `Traces:` with at least one existing `REQ-*` or `DEC-*`; optional additional `AC-*` references are allowed;
- optional `Depends-on:` containing only existing `TASK-*` IDs and forming no cycle;
- scope and expected files;
- forbidden paths or areas;
- ordered implementation steps;
- task-local technical details;
- at least one nested heading whose title starts with `Test scenario`.

Every test scenario describes setup, action, and expected result in free text and has `Traces:` to at least one `AC-*` or `REQ-*`. Test scenarios have no IDs. Together the tasks and scenarios cover every active `REQ-*`, `DEC-*`, and `AC-*` from the specification.

Use the exact English field names `Traces:` and `Depends-on:` regardless of document language. IDs are positive and permanent: preserve an ID for the same task, never issue zero, never use alternate padded spellings of one numeric identity in the same document, and never reuse a removed or rejected identity.

Do not include `done when`, verification commands, manual checks, non-test verification, or documentation tasks. The only verification described by this plan is automated test scenarios. Leave `Open questions` empty when there are no explicitly deferred questions; never write `None`.
