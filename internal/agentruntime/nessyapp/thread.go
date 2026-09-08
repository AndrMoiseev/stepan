package nessyapp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// runtimeThread is the per-logical-thread lifecycle boundary. Production owns
// one Process, Connection, ACP session, and turn runner behind each instance;
// the interface keeps lifecycle tests independent of an installed Nessy CLI.
type runtimeThread interface {
	RunTurn(string) (json.RawMessage, error)
	Cancel() error
	BeginClose()
	Done() <-chan struct{}
	Err() error
	Close() error
}

type nessyThread struct {
	process    *Process
	connection *Connection
	runner     *turnRunner

	closeOnce sync.Once
	closeErr  error
}

func startNessyThread(ctx context.Context, config Config, threadConfig agentruntime.ThreadConfig) (runtimeThread, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	process := NewProcess(config, threadConfig.ArtifactRoot)
	if err := process.Start(); err != nil {
		_ = process.Close()
		return nil, err
	}
	stopCancellation := make(chan struct{})
	cancellationDone := make(chan struct{})
	go func() {
		defer close(cancellationDone)
		select {
		case <-ctx.Done():
			_ = process.Close()
		case <-stopCancellation:
		}
	}()
	defer func() {
		close(stopCancellation)
		<-cancellationDone
	}()
	connection, err := OpenConnection(process)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = connection.Close()
		_ = process.Close()
		return nil, err
	}
	canonicalConfig := threadConfig.Clone()
	canonicalConfig.Workspace = process.WorkspaceRoot()
	canonicalConfig.ArtifactRoot = process.WritableRoot()
	runner, err := newTurnRunner(connection, canonicalConfig)
	if err != nil {
		_ = connection.Close()
		_ = process.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = connection.Close()
		_ = process.Close()
		return nil, err
	}
	return &nessyThread{process: process, connection: connection, runner: runner}, nil
}

func (thread *nessyThread) RunTurn(prompt string) (json.RawMessage, error) {
	return thread.runner.run(prompt)
}

func (thread *nessyThread) Cancel() error {
	return thread.connection.sendNotification("session/cancel", sessionParams{SessionID: thread.connection.SessionID()})
}

func (thread *nessyThread) Done() <-chan struct{} { return thread.connection.Done() }
func (thread *nessyThread) Err() error            { return thread.connection.Err() }

func (thread *nessyThread) BeginClose() { thread.connection.checkpointClose() }

func (thread *nessyThread) Close() error {
	thread.closeOnce.Do(func() {
		thread.BeginClose()
		thread.closeErr = errors.Join(thread.connection.Close(), thread.process.Close())
	})
	return thread.closeErr
}
