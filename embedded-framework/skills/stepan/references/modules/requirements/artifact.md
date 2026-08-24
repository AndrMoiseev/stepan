# OpenSpec-style `requirements.md` delta contract

Keep `Purpose` and `Boundaries and assumptions` non-empty. Include at least one
non-empty delta section and omit every unused delta section. Additional useful
sections are allowed.

```text
# <feature> Specification Delta
## Purpose
<short description of the behavior changed by this specification>

## ADDED Requirements
### Requirement: <new descriptive name>
The <system> SHALL <one observable behavior>.

#### Scenario: <specific case>
- **GIVEN** <initial state>
- **WHEN** <trigger or condition>
- **THEN** <observable outcome>
- **AND** <additional outcome or condition>

## MODIFIED Requirements
### Requirement: <exact existing name, or RENAMED target>
The <system> SHALL <complete updated observable behavior>.

#### Scenario: <specific case>
- **WHEN** <trigger or condition>
- **THEN** <observable outcome>

## REMOVED Requirements
### Requirement: <exact existing name>
**Reason**: <why the requirement is removed>
**Migration**: <how affected users, data, or callers transition>

## RENAMED Requirements
- FROM: `### Requirement: <exact existing name>`
- TO: `### Requirement: <new name>`

## Boundaries and assumptions
<scope limits and declared assumptions>
```

Use the sections as follows:

- `ADDED` contains only behavior that does not exist in the baseline.
- `MODIFIED` contains the complete updated requirement block, including all
  retained behavior and scenarios, rather than only the changed fragment. Use
  the exact baseline name unless the same change renames it; then use the
  `RENAMED` target name.
- `REMOVED` identifies the exact baseline requirement and supplies both
  `Reason` and `Migration`.
- `RENAMED` records only the naming part of a change as an adjacent `FROM`/`TO`
  pair. If the same requirement's behavior changes too, also include the
  complete updated requirement under `MODIFIED` using the new name.

For `ADDED` and `MODIFIED`, keep requirement names descriptive and shorter than
50 characters. Treat the exact case-sensitive text after `### Requirement:` as
the requirement identifier. Immediately follow every requirement heading with
one `SHALL` statement and at least one `#### Scenario:` block.

Use `GIVEN` only when an initial state is needed. Require `WHEN` and `THEN` in
every scenario; use `AND` only for additional conditions or outcomes. Do not
place the same requirement in conflicting delta sections: a renamed source
cannot also be removed, and a rename target cannot also be added.

Use these canonical references when another artifact or review cites a delta:

- `ADDED Requirement "<name>"`
- `MODIFIED Requirement "<name>"`
- `REMOVED Requirement "<name>"`
- `RENAMED Requirement "<old name>" -> "<new name>"`
