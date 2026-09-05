package qwenapp

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// runtimeThread is the per-logical-thread lifecycle boundary. Production owns
// one Process, Connection, ACP session, and turn runner behind each instance;
// the interface keeps lifecycle tests independent of an installed Qwen CLI.
type runtimeThread interface {
	RunTurn(string) (json.RawMessage, error)
	Cancel() error
	BeginClose()
	Done() <-chan struct{}
	Err() error
	Close() error
}

type qwenThread struct {
	process    *Process
	connection *Connection
	runner     *turnRunner

	closeOnce sync.Once
	closeErr  error
}

func startQwenThread(config Config, threadConfig agentruntime.ThreadConfig) (runtimeThread, error) {
	process := NewProcess(config, threadConfig.ArtifactRoot)
	if err := process.Start(); err != nil {
		_ = process.Close()
		return nil, err
	}
	connection, err := OpenConnection(process)
	if err != nil {
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
	return &qwenThread{process: process, connection: connection, runner: runner}, nil
}

func (thread *qwenThread) RunTurn(prompt string) (json.RawMessage, error) {
	return thread.runner.run(prompt)
}

func (thread *qwenThread) Cancel() error {
	return thread.connection.sendNotification("session/cancel", sessionParams{SessionID: thread.connection.SessionID()})
}

func (thread *qwenThread) Done() <-chan struct{} { return thread.connection.Done() }
func (thread *qwenThread) Err() error            { return thread.connection.Err() }

func (thread *qwenThread) BeginClose() { thread.connection.checkpointClose() }

func (thread *qwenThread) Close() error {
	thread.closeOnce.Do(func() {
		thread.BeginClose()
		thread.closeErr = errors.Join(thread.connection.Close(), thread.process.Close())
	})
	return thread.closeErr
}
