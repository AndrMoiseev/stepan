// Package store contains durable storage for implementation-flow runs.
// It owns ~/.stepan/runs/<run-id>/ and immutable related files; JSONL and
// SQLite are layered on top of this layout.
package store
