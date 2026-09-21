//go:build process_integration

package checkexec

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// trackedContext exposes active cancellation registrations through the public
// context.AfterFunc protocol, without depending on context's private fields.
type trackedContext struct {
	context.Context
	active atomic.Int32
}

func (ctx *trackedContext) Value(any) any { return nil }

func (ctx *trackedContext) AfterFunc(f func()) func() bool {
	ctx.active.Add(1)
	stop := context.AfterFunc(ctx.Context, f)
	return func() bool {
		if stop() {
			ctx.active.Add(-1)
			return true
		}
		return false
	}
}

func TestTimedCommandReleasesCancellationRegistration(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &trackedContext{Context: parent}
	command := helperCommand(t.TempDir(), nil, nil)
	command.Timeout = time.Minute
	if _, err := RunContext(ctx, command); err != nil {
		t.Fatal(err)
	}
	if active := ctx.active.Load(); active != 0 {
		t.Fatalf("completed command retains %d cancellation registrations", active)
	}
}
