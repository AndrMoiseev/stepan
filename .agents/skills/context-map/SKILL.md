---
name: context-map
description: Generate or update the repository's Go package context map and Archify dependency diagram in docs/architecture/context-map/. Use when asked for a context map or module dependency map of this repository.
---

# Context map

Keep every artifact of this map under `docs/architecture/context-map/`:

- `context-map.md` is the main document.
- `archify/` contains the Archify specification, checked HTML, exported diagram image, and validation or browser evidence.

1. Inventory every directory containing a Go package under `cmd/` and `internal/`, including test and CI packages. Treat one package directory as one module. Read `go.mod`, package declarations, and current source; use existing architecture documents only as context.
2. Read `arch-go.yml` as the source of truth for **configured internal dependency rules**. Expand package regexes against the inventory. For each package, record targets allowed by applicable `shouldOnlyDependsOn` rules after exclusions from `shouldNotDependsOn`. A blanket prohibition means no outgoing arrow. If neither an allowed-target list nor a blanket prohibition applies, mark its outgoing rules as unspecified. Configured permission is distinct from an observed source import; omit external dependencies.
3. Use `$archify` to generate an `architecture` diagram of the module dependencies. Keep the source JSON and all generated files in `archify/`. An overview may group related packages and show selected edges for readability; label that scope explicitly. Keep the complete per-package inventory and configured dependency information in `context-map.md`. In the diagram, `A → B` means `A` may import `B` under `arch-go.yml`. Do not use Mermaid for the module diagram.
4. Deliver the Archify HTML using its validation workflow, export a PNG or SVG image of the diagram from the generated viewer, and check the rendered result. Embed the image in `context-map.md` with a relative path such as `archify/module-dependencies.svg`; put a relative link to the interactive HTML beside it. Keep the meaning of arrows and any grouping or omissions next to the image.
5. Describe **every** module's purpose, concrete responsibilities, and boundary in separate fields in `context-map.md`. A boundary states what the module owns and where another module takes over; derive claims from current source and rules. Reconcile added, removed, and renamed packages and rules on updates.
6. Before handoff, compare the inventory with the module descriptions and complete dependency information, compare each drawn arrow with `arch-go.yml`, verify the Markdown image and HTML links resolve, and report any configuration gaps rather than filling them with assumed dependencies.
