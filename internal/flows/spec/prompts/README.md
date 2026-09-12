# Prompt assets

System contracts, role instructions, and capability modules are immutable
embedded defaults resolved through logical IDs by `PromptCatalog`. Control-plane
composition is defined in Go and does not depend on physical asset paths.

Every author and reviewer thread is composed through `PromptCatalog`; there is
no shared bootstrap prompt or second lifecycle.
