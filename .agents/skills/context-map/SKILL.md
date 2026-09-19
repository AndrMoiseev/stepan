---
name: context-map
description: Generate or update docs/architecture/context_map.md with every Go package's purpose, responsibilities, boundaries, and the internal dependency rules from arch-go.yml. Use when asked for a context map or module dependency map of this repository.
---

# Context map

Create or update `docs/architecture/context_map.md` from the current repository state.

1. Inventory every directory containing Go package files under `cmd/` and `internal/`, including test and CI packages. Treat one package directory as one module. Read `go.mod` for the module path and inspect package declarations and source to understand each module. Use existing architecture documents as context, then verify claims against current code.
2. Read `arch-go.yml` as the source of truth for the **configured internal dependency rules**. Expand its package regexes against the inventory. For each package, show the internal targets listed by an applicable `shouldOnlyDependsOn` rule, excluding targets forbidden by an applicable `shouldNotDependsOn` rule. A prohibition on all internal packages means no outgoing arrow. If neither an allowed-target list nor a blanket internal-import prohibition applies, label the outgoing dependencies as unspecified by the configuration. Do not infer arrows from source imports or draw external dependencies.
3. Write a Mermaid diagram containing every inventoried module. Make arrow direction explicit: `A --> B` means `A` may import `B` according to `arch-go.yml`, not that it currently does. Distinguish modules whose outgoing rules are unspecified. Keep test and CI packages visible even if they have no configured edges.
4. Describe **every** module's purpose, concrete responsibilities, and boundary in separate fields. Derive this text from current source. A boundary states what the module owns and where another module takes over; avoid claims of isolation or restrictions that `arch-go.yml` does not establish.
5. On updates, reconcile added, removed, and renamed packages and rules. Preserve useful explanatory text only while it remains accurate. Finish by comparing the module inventory with diagram nodes and descriptions, and comparing every diagram arrow with `arch-go.yml`. Report configuration gaps rather than filling them with assumed dependencies.

The output file is the deliverable. Keep its scope and legend near the diagram so readers can distinguish configured permission from observed imports.
