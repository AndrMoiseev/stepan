# Prompt assets

System contracts, role instructions, and capability modules are immutable
embedded defaults resolved through logical IDs by `PromptCatalog`. Control-plane
composition is defined in Go and does not depend on physical asset paths.

Every author and reviewer thread is composed through `PromptCatalog`; there is
no shared bootstrap prompt or second lifecycle.

Document formats live in `capabilities/<stage>/document.md`. Review report
formats live in `capabilities/spec/review-document.md` and
`capabilities/plan/review-document.md`; the neighboring `review.md` files describe
review methodology and decision policy. The catalog includes both fragments in
each reviewer prompt. Machine-readable contracts are validated in Go, so changes
to required fields or lifecycle semantics must also update the validators.
