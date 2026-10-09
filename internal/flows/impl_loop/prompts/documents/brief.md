# Assignment brief format

Return the brief body as non-empty Markdown in the `brief` field of `brief_ready`; `task_ids` is non-empty and identifies the selected tasks in controller order.

The body is a self-contained contract derived from the complete specification for the selected assignment. No fixed Markdown section headings are required. Do not copy project rules into the brief; the controller supplies the current rules separately.

Return only the body, without YAML frontmatter. The controller publishes the document with `assignment_id`, `version`, and `task_ids` frontmatter and owns these values. Refinement produces a new version for the same assignment and task IDs.
