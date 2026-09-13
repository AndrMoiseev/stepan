package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

var (
	// ErrSessionOwnerClosed means that the process-local owner has been closed.
	// A resumed run must use a new owner and therefore new provider sessions.
	ErrSessionOwnerClosed = errors.New("implementation session owner is closed")
	ErrSessionClosed      = errors.New("implementation agent session is closed")
)

// SessionOwner owns the provider conversations for one implementation run in
// one working process. It deliberately has no persistence or provider resume
// capability: a process restart creates a new owner from durable run state.
//
// The owner keeps only sessions whose scope permits a continuation. Explorer
// sessions are created anew for every request. The controller supplies result
// messages to the continuing source session in a later turn.
type SessionOwner struct {
	prepared PreparedRuntimes
	base     agentruntime.ThreadConfig

	mu         sync.Mutex
	closed     bool
	persistent map[sessionKey]*AgentSession
	sessions   map[*AgentSession]struct{}
}

type sessionScope string

const (
	sessionScopeOrchestrator sessionScope = "orchestrator"
	sessionScopeAssignment   sessionScope = "assignment"
	sessionScopeFinalReview  sessionScope = "final_review"
)

type sessionKey struct {
	scope sessionScope
	role  ResponseRole
	id    string
}

// AgentSession is one controller-owned provider thread. Its only interaction
// surface is a sequential turn; security policy and output schema were fixed
// when the session was opened.
type AgentSession struct {
	Role ResponseRole

	runtime agentruntime.Runtime
	thread  agentruntime.Thread
	owner   *SessionOwner

	mu     sync.Mutex
	closed bool
}

// NewSessionOwner builds the process-local owner for a single implementation
// run. The base config supplies the workspace and optional artifact root; the
// owner supplies each role's immutable bootstrap message, schema, and write
// permission when opening a session.
func NewSessionOwner(prepared PreparedRuntimes, base agentruntime.ThreadConfig) (*SessionOwner, error) {
	if !filepath.IsAbs(base.Workspace) {
		return nil, errors.New("implementation session owner requires an absolute workspace")
	}
	if base.ArtifactRoot != "" && !filepath.IsAbs(base.ArtifactRoot) {
		return nil, errors.New("implementation session owner requires an absolute artifact root")
	}
	return &SessionOwner{
		prepared:   prepared,
		base:       base.Clone(),
		persistent: make(map[sessionKey]*AgentSession),
		sessions:   make(map[*AgentSession]struct{}),
	}, nil
}

// Orchestrator returns the one conversation for this owner. Repeated calls in
// the same working process continue that conversation rather than starting a
// provider-specific resumed session.
func (owner *SessionOwner) Orchestrator(ctx context.Context, start RoleStartContext) (*AgentSession, error) {
	return owner.persistentSession(ctx, sessionKey{scope: sessionScopeOrchestrator, role: ResponseRoleOrchestrator}, start)
}

// Assignment returns the briefer, implementer, or task-reviewer conversation
// for one assignment. A different assignment gets an entirely new session for
// each role, while follow-up work for this assignment keeps its conversation.
func (owner *SessionOwner) Assignment(ctx context.Context, assignmentID implementationstate.AssignmentID, role ResponseRole, start RoleStartContext) (*AgentSession, error) {
	if strings.TrimSpace(string(assignmentID)) == "" {
		return nil, errors.New("implementation assignment session requires an assignment ID")
	}
	if role != ResponseRoleBriefer && role != ResponseRoleImplementer && role != ResponseRoleTaskReviewer {
		return nil, fmt.Errorf("implementation assignment session does not support role %q", role)
	}
	return owner.persistentSession(ctx, sessionKey{scope: sessionScopeAssignment, role: role, id: string(assignmentID)}, start)
}

// Explorer opens a fresh conversation for this single research request. It is
// intentionally not cached, so a new request cannot inherit another request's
// history. Close it once its result has been delivered to the source session.
func (owner *SessionOwner) Explorer(ctx context.Context, start RoleStartContext) (*AgentSession, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil, ErrSessionOwnerClosed
	}
	return owner.newSessionLocked(ctx, ResponseRoleExplorer, start)
}

// FinalReviewer returns the conversation for one final-review round. The same
// round can continue after an Explorer request; a new round always receives a
// new independent conversation.
func (owner *SessionOwner) FinalReviewer(ctx context.Context, roundID string, start RoleStartContext) (*AgentSession, error) {
	if strings.TrimSpace(roundID) == "" {
		return nil, errors.New("implementation final-review session requires a round ID")
	}
	return owner.persistentSession(ctx, sessionKey{scope: sessionScopeFinalReview, role: ResponseRoleFinalReviewer, id: roundID}, start)
}

func (owner *SessionOwner) persistentSession(ctx context.Context, key sessionKey, start RoleStartContext) (*AgentSession, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil, ErrSessionOwnerClosed
	}
	if session, ok := owner.persistent[key]; ok {
		return session, nil
	}
	session, err := owner.newSessionLocked(ctx, key.role, start)
	if err != nil {
		return nil, err
	}
	owner.persistent[key] = session
	return session, nil
}

func (owner *SessionOwner) newSessionLocked(ctx context.Context, role ResponseRole, start RoleStartContext) (*AgentSession, error) {
	if start.Role != role {
		return nil, fmt.Errorf("implementation session role %q does not match start context role %q", role, start.Role)
	}
	prepared, ok := owner.prepared.Role(string(role))
	if !ok {
		return nil, fmt.Errorf("implementation runtime is not prepared for role %q", role)
	}
	config := owner.base.Clone()
	config.WorkspaceWriteAllowed = role == ResponseRoleOrchestrator || role == ResponseRoleImplementer
	config, err := ThreadConfigForRoleContext(start, config)
	if err != nil {
		return nil, fmt.Errorf("prepare implementation session for role %q: %w", role, err)
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate implementation session for role %q: %w", role, err)
	}
	runtime, err := prepared.Start(ctx)
	if err != nil {
		return nil, err
	}
	thread, err := runtime.StartThread(config)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start implementation session for role %q: %w", role, err), runtime.Close())
	}
	session := &AgentSession{Role: role, runtime: runtime, thread: thread, owner: owner}
	owner.sessions[session] = struct{}{}
	return session, nil
}

// RunTurn continues this role's provider conversation. Calls are serialized
// because the provider-neutral Runtime contract permits only one active turn.
func (session *AgentSession) RunTurn(message string) (json.RawMessage, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, ErrSessionClosed
	}
	return session.runtime.RunTurn(session.thread, message)
}

// Interrupt asks the provider to stop this session's active turn. Each
// implementation session owns its runtime, so this does not interrupt a
// different role's conversation. It intentionally does not take the turn
// mutex: RunTurn holds that mutex while the provider is running.
func (session *AgentSession) Interrupt() error {
	if session == nil || session.runtime == nil {
		return ErrSessionClosed
	}
	return session.runtime.Interrupt()
}

// Close releases every session opened by this owner. It is safe to call more
// than once. A closed owner cannot reopen old provider conversations.
func (owner *SessionOwner) Close() error {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closed = true
	sessions := make([]*AgentSession, 0, len(owner.sessions))
	for session := range owner.sessions {
		sessions = append(sessions, session)
	}
	owner.sessions = make(map[*AgentSession]struct{})
	owner.persistent = make(map[sessionKey]*AgentSession)
	owner.mu.Unlock()

	var closeErr error
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.close())
	}
	return closeErr
}

// Close releases one session early, which is useful for the one-request
// Explorer scope. Persistent sessions are removed, so a later request opens a
// fresh conversation rather than returning a closed handle.
func (session *AgentSession) Close() error {
	if session.owner == nil {
		return session.close()
	}
	return session.owner.release(session)
}

func (owner *SessionOwner) release(session *AgentSession) error {
	owner.mu.Lock()
	delete(owner.sessions, session)
	for key, current := range owner.persistent {
		if current == session {
			delete(owner.persistent, key)
		}
	}
	owner.mu.Unlock()
	return session.close()
}

func (session *AgentSession) close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	return errors.Join(session.runtime.CloseThread(session.thread), session.runtime.Close())
}
