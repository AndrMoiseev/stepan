//go:build process_integration

package codexapp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"testing/synctest"
)

// The peer can read a response and complete the turn before our Write returns.
func TestRunTurnWaitsForDeliveredApprovalWrite(t *testing.T) {
	workspace := canonicalTempDir(t)
	writeFailure := errors.New("approval write failed after delivery")
	for _, test := range []struct {
		name            string
		writeErr        error
		closeConnection bool
		wantErr         error
	}{
		{name: "successful write"},
		{name: "failed write", writeErr: writeFailure, wantErr: writeFailure},
		{name: "connection closes during write", closeConnection: true, wantErr: ErrConnectionClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clientSide, serverSide := net.Pipe()
				defer clientSide.Close()
				defer serverSide.Close()
				release := make(chan struct{})
				defer close(release)
				writer := &delayedApprovalWriter{Writer: clientSide, release: release, writeErr: test.writeErr}
				connection := newConnection(NewTransport(clientSide, writer), Handler{})
				defer connection.Close()
				server := NewTransport(serverSide, serverSide)
				serverErr := make(chan error, 1)
				go serveWorkspaceWriteTurn(t, server, serverErr, workspaceWriteTurnCase{
					workspaceWriteAllowed: true,
					target:                func(workspace, _ string) string { return filepath.Join(workspace, "source.go") },
					wantDecision:          DecisionAccept, wantSandbox: "workspaceWrite", wantWritableRootCount: 1,
				}, workspace, "")
				config := testThreadConfig(workspace)
				config.WorkspaceWriteAllowed = true
				thread, err := connection.StartThread(workspace, config)
				if err != nil {
					t.Fatal(err)
				}
				turnErr := make(chan error, 1)
				go func() { _, err := connection.RunTurn(thread, "edit files"); turnErr <- err }()
				if err := <-serverErr; err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				select {
				case err := <-turnErr:
					t.Fatalf("turn completed before approval write returned: %v", err)
				default:
				}
				if test.closeConnection {
					_ = connection.Close()
				} else {
					release <- struct{}{}
				}
				if err := <-turnErr; !errors.Is(err, test.wantErr) {
					t.Fatalf("turn error = %v, want %v", err, test.wantErr)
				}
			})
		})
	}
}

func TestTurnCompletionRejectsUndecidedApproval(t *testing.T) {
	run := &turnRun{pending: make(map[string]bool), wake: make(chan struct{}, 1)}
	if err := run.addPending("approval"); err != nil {
		t.Fatal(err)
	}
	if err := run.push(Message{Method: "turn/completed"}); err == nil {
		t.Fatal("terminal notification accepted an undecided approval")
	}
}

type delayedApprovalWriter struct {
	io.Writer
	release  <-chan struct{}
	writeErr error
}

func (w *delayedApprovalWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	message, parseErr := ParseMessage(bytes.TrimSpace(data))
	if err == nil && parseErr == nil && message.Kind == Response && message.ID.Key() == StringID("approval").Key() {
		<-w.release
		err = w.writeErr
	}
	return n, err
}
