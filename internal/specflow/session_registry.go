package specflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// SessionRequest identifies a provider conversation without making its opaque
// handle durable. ArtifactRoot is used only when a new conversation is
// created; a reused conversation keeps the root from its immutable config.
type SessionRequest struct {
	FeatureID string
	Role      Role
	Config    agentruntime.ThreadConfig
}

// RegisteredSession is a process-local lease of one role conversation.
type RegisteredSession struct {
	Thread       agentruntime.Thread
	ArtifactRoot string
	Reused       bool
}

type sessionKey struct {
	featureID string
	role      Role
}

type sessionEntry struct {
	thread       agentruntime.Thread
	artifactRoot string
	stage        Stage
	leased       bool
}

// SessionRegistry owns live provider conversations for one process. Durable
// state contains no reference to these entries.
type SessionRegistry struct {
	runner     dialogueRunner
	repository SessionLifecycleRepository

	mu      sync.Mutex
	entries map[sessionKey]*sessionEntry
	closed  map[string]bool
	stopped bool
}

func NewSessionRegistry(runner dialogueRunner, repository SessionLifecycleRepository) (*SessionRegistry, error) {
	if runner == nil || repository == nil {
		return nil, fmt.Errorf("create session registry: runner and repository are required")
	}
	return &SessionRegistry{
		runner: runner, repository: repository,
		entries: make(map[sessionKey]*sessionEntry), closed: make(map[string]bool),
	}, nil
}

// Acquire returns the existing conversation for feature/role when available.
// A role can have only one active lease at a time.
func (r *SessionRegistry) Acquire(request SessionRequest) (RegisteredSession, error) {
	if err := ValidateFeatureID(request.FeatureID); err != nil {
		return RegisteredSession{}, err
	}
	if !request.Role.Valid() {
		return RegisteredSession{}, domainError("role", request.Role)
	}
	if err := request.Config.Validate(); err != nil {
		return RegisteredSession{}, fmt.Errorf("acquire %s session: %w", request.Role, err)
	}
	if strings.TrimSpace(request.Config.ArtifactRoot) == "" {
		return RegisteredSession{}, fmt.Errorf("acquire %s session: artifact root is required", request.Role)
	}
	stage, err := stageForRole(request.Role)
	if err != nil {
		return RegisteredSession{}, err
	}

	key := sessionKey{featureID: request.FeatureID, role: request.Role}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return RegisteredSession{}, agentruntime.ErrRuntimeClosed
	}
	if entry := r.entries[key]; entry != nil {
		if entry.leased {
			r.mu.Unlock()
			return RegisteredSession{}, fmt.Errorf("acquire %s session for %s: session is already active", request.Role, request.FeatureID)
		}
		entry.leased = true
		r.closed[request.FeatureID] = false
		root, thread := entry.artifactRoot, entry.thread
		r.mu.Unlock()
		if filepathDistinct(request.Config.ArtifactRoot, root) {
			_ = removeArtifact(request.Config.ArtifactRoot)
		}
		return RegisteredSession{Thread: thread, ArtifactRoot: root, Reused: true}, nil
	}
	r.mu.Unlock()

	thread, err := r.runner.StartThread(request.Config.Clone())
	if err != nil {
		return RegisteredSession{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		_ = r.runner.CloseThread(thread)
		return RegisteredSession{}, agentruntime.ErrRuntimeClosed
	}
	if existing := r.entries[key]; existing != nil {
		_ = r.runner.CloseThread(thread)
		return RegisteredSession{}, fmt.Errorf("acquire %s session for %s: session was created concurrently", request.Role, request.FeatureID)
	}
	r.entries[key] = &sessionEntry{thread: thread, artifactRoot: request.Config.ArtifactRoot, stage: stage, leased: true}
	r.closed[request.FeatureID] = false
	return RegisteredSession{Thread: thread, ArtifactRoot: request.Config.ArtifactRoot}, nil
}

func filepathDistinct(left, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && !strings.EqualFold(left, right)
}

func stageForRole(role Role) (Stage, error) {
	switch role {
	case RoleIntentAuthor:
		return StageIntent, nil
	case RoleSpecAuthor, RoleSpecReviewer:
		return StageSpec, nil
	case RolePlanAuthor, RolePlanReviewer:
		return StagePlan, nil
	default:
		return "", domainError("role", role)
	}
}

func (r *SessionRegistry) Release(featureID string, role Role) error {
	key := sessionKey{featureID: featureID, role: role}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	if entry == nil {
		return nil
	}
	entry.leased = false
	return nil
}

func (r *SessionRegistry) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return r.runner.StartThread(config)
}

func (r *SessionRegistry) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	output, err := r.runner.RunTurn(thread, prompt)
	if err != nil && errors.Is(err, agentruntime.ErrThreadFailed) {
		err = errors.Join(err, r.CloseThread(thread))
	}
	return output, err
}

// CloseThread is the hard-close failure path used after a failed turn. Normal
// engine detachment uses Release and preserves the conversation.
func (r *SessionRegistry) CloseThread(thread agentruntime.Thread) error {
	r.mu.Lock()
	var found sessionKey
	var root string
	for key, entry := range r.entries {
		if sameRuntimeThread(entry.thread, thread) {
			found, root = key, entry.artifactRoot
			delete(r.entries, key)
			break
		}
	}
	r.mu.Unlock()
	err := r.runner.CloseThread(thread)
	if found.featureID != "" {
		err = errors.Join(err, removeArtifact(root))
	}
	return err
}

func sameRuntimeThread(left, right agentruntime.Thread) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	return leftType == rightType && leftType.Comparable() && reflect.ValueOf(left).Interface() == reflect.ValueOf(right).Interface()
}

// CloseFeature is idempotent. It closes every role conversation, discards
// session artifacts through the repository, and leaves flow_status untouched.
func (r *SessionRegistry) CloseFeature(featureID string) error {
	r.mu.Lock()
	if r.closed[featureID] {
		r.mu.Unlock()
		return nil
	}
	artifacts := make([]SessionArtifact, 0)
	threads := make([]agentruntime.Thread, 0)
	for key, entry := range r.entries {
		if key.featureID != featureID {
			continue
		}
		threads = append(threads, entry.thread)
		artifacts = append(artifacts, SessionArtifact{Stage: entry.stage, Role: key.role, ArtifactRoot: entry.artifactRoot})
		delete(r.entries, key)
	}
	r.closed[featureID] = true
	r.mu.Unlock()

	var result error
	for _, thread := range threads {
		result = errors.Join(result, r.runner.CloseThread(thread))
	}
	_, repositoryErr := r.repository.CloseFeatureSession(SessionCloseRequest{FeatureID: featureID, Artifacts: artifacts})
	if repositoryErr != nil {
		for _, artifact := range artifacts {
			result = errors.Join(result, removeArtifact(artifact.ArtifactRoot))
		}
	}
	return errors.Join(result, repositoryErr)
}

func (r *SessionRegistry) Close() error {
	r.mu.Lock()
	features := make(map[string]struct{})
	for key := range r.entries {
		features[key.featureID] = struct{}{}
	}
	r.mu.Unlock()
	var result error
	for featureID := range features {
		result = errors.Join(result, r.CloseFeature(featureID))
	}
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	return result
}
