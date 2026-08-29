// Package conformance contains provider-neutral runtime contract checks used
// by adapter tests. It intentionally depends only on agentruntime.
package conformance

import (
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// Factory starts a real adapter backed by that adapter's fake transport and
// returns one valid immutable thread configuration.
type Factory func(*testing.T) (agentruntime.Runtime, agentruntime.ThreadConfig)

// ClosedThread verifies a lifecycle rule shared by every provider: a closed
// logical thread cannot accept another input, while closing the runtime remains
// safe afterwards.
func ClosedThread(t *testing.T, factory Factory) {
	t.Helper()
	runtime, config := factory(t)
	t.Cleanup(func() { _ = runtime.Close() })
	thread, err := runtime.StartThread(config)
	if err != nil {
		t.Fatalf("start thread: %v", err)
	}
	if err := runtime.CloseThread(thread); err != nil {
		t.Fatalf("close thread: %v", err)
	}
	if _, err := runtime.RunTurn(thread, "must not run"); err == nil {
		t.Fatal("closed thread accepted a turn")
	}
}
