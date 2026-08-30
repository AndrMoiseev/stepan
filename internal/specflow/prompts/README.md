# Prompt assets

System contracts, role instructions, and capability modules are immutable
embedded defaults resolved through logical IDs by `PromptCatalog`. Control-plane
composition is defined in Go and does not depend on physical asset paths.

`bootstrap.md` is retained temporarily as a legacy migration asset until the
intent-only lifecycle is removed. It is no longer embedded or used to compose
new threads; the compatibility `BootstrapPrompt` function delegates to
`PromptCatalog`.
