package qwenapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// OpenConnection performs initialize and session/new before publishing a
// usable session. The Process must already be started and contained. Every
// failure closes that process before this function returns.
func OpenConnection(process *Process) (*Connection, error) {
	workspace, startupRoot, err := validateProcessStartupContract(process)
	if err != nil {
		if process != nil {
			_ = process.Close()
		}
		return nil, err
	}
	connection := newConnection(newTransport(process.Stdout(), process.Stdin()), process, connectionHandler{})
	connection.configureFilePolicy(workspace, process.WritableRoot())
	if err := connection.preflight(workspace, startupRoot); err != nil {
		connection.fail(err)
		return nil, connection.Err()
	}
	return connection, nil
}

// validateProcessStartupContract proves the part of the startup-root contract
// available without a model turn: the contained child was launched with the
// canonical root in the exact fixed argv and with the canonical Git cwd. ACP
// cannot prove that an arbitrary executable honors that argv. If an agent
// voluntarily reports startupRoot in _meta, preflight validates it below;
// otherwise behavioral proof remains the shared conformance/manual canary
// required by REQ-018.
func validateProcessStartupContract(process *Process) (string, string, error) {
	if process == nil {
		return "", "", fmt.Errorf("%w: contained process is not started", ErrIncompatible)
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if !process.started || process.closed || process.stdin == nil || process.stdout == nil || process.command == nil ||
		process.workspaceRoot == "" || process.artifactRoot == "" {
		return "", "", fmt.Errorf("%w: contained process is not started", ErrIncompatible)
	}
	expectedArgs := qwenArgs(process.config.JSONContract, process.artifactRoot)
	if process.command.Dir != process.workspaceRoot || len(process.command.Args) != len(expectedArgs)+1 {
		return "", "", withDiagnosticContext(fmt.Errorf("%w: Qwen startup-root contract", ErrIncompatible), diagnosticStartupRoot)
	}
	for index := range expectedArgs {
		if process.command.Args[index+1] != expectedArgs[index] {
			return "", "", withDiagnosticContext(fmt.Errorf("%w: Qwen startup-root contract", ErrIncompatible), diagnosticStartupRoot)
		}
	}
	return process.workspaceRoot, process.artifactRoot, nil
}

func (connection *Connection) preflight(workspace, startupRoot string) error {
	var initialized initializeResponse
	if err := connection.callAndCommit("initialize", initializeParams{
		ProtocolVersion:    acpProtocolVersion,
		ClientCapabilities: clientCapabilities{FS: fileSystemCapabilities{ReadTextFile: true}},
		ClientInfo: implementationInfo{
			Name: "stepan", Title: "Stepan", Version: "0",
		},
	}, &initialized, func() error {
		if initialized.ProtocolVersion != acpProtocolVersion {
			return withDiagnosticContext(fmt.Errorf("%w: initialize protocolVersion", ErrIncompatible), diagnosticProtocolVersion)
		}
		if err := validateMandatoryCapabilities(initialized.AgentCapabilities); err != nil {
			return err
		}
		if inventory, present, err := preflightStatusFromMeta(startupRoot, initialized.Meta, initialized.AgentCapabilities.Meta); err != nil {
			return err
		} else if present {
			if err := validateToolInventory(inventory); err != nil {
				return err
			}
			if err := connection.recordPreflightInventory(inventory); err != nil {
				return err
			}
		}
		connection.mu.Lock()
		connection.initialized = true
		connection.mu.Unlock()
		return nil
	}); err != nil {
		return incompatibleCall("initialize", err)
	}

	var session newSessionResponse
	if err := connection.callAndCommit("session/new", newSessionParams{CWD: workspace, MCPServers: []any{}}, &session, func() error {
		if !validAgentIdentity(session.SessionID) {
			return withDiagnosticContext(fmt.Errorf("%w: session/new sessionId", ErrIncompatible), diagnosticSessionLifecycle)
		}
		if inventory, present, err := preflightStatusFromMeta(startupRoot, session.Meta); err != nil {
			return err
		} else if present {
			if err := validateToolInventory(inventory); err != nil {
				return err
			}
			if err := connection.recordPreflightInventory(inventory); err != nil {
				return err
			}
		}
		connection.mu.Lock()
		defer connection.mu.Unlock()
		if connection.sessionID != "" || connection.ready {
			return fmt.Errorf("%w: duplicate session/new", ErrProtocol)
		}
		connection.sessionID = session.SessionID
		connection.ready = true
		return nil
	}); err != nil {
		return incompatibleCall("session/new", err)
	}
	return nil
}

// ACP v1 makes session/new, session/prompt, session/cancel, session/update and
// session/request_permission baseline methods rather than individual boolean
// flags. Stepan still requires explicit standard promptCapabilities and
// sessionCapabilities objects so a defaulted/empty agentCapabilities response
// is not accepted as evidence. session/new is exercised during preflight;
// prompt/cancel/permission behavior is enforced by the strict dispatcher and
// by the conformance scenarios because probing it would require a model turn.
func validateMandatoryCapabilities(capabilities *agentCapabilities) error {
	if capabilities == nil {
		return withDiagnosticContext(fmt.Errorf("%w: missing agentCapabilities", ErrIncompatible), diagnosticAgentCapabilities)
	}
	if capabilities.PromptCapabilities == nil {
		return withDiagnosticContext(fmt.Errorf("%w: missing promptCapabilities", ErrIncompatible), diagnosticPromptCapabilities)
	}
	if capabilities.SessionCapabilities == nil {
		return withDiagnosticContext(fmt.Errorf("%w: missing sessionCapabilities", ErrIncompatible), diagnosticSessionCapabilities)
	}
	return nil
}

func incompatibleCall(method string, cause error) error {
	if errors.Is(cause, ErrConnectionClosed) {
		return cause
	}
	if errors.Is(cause, ErrIncompatible) {
		return cause
	}
	context := diagnosticNone
	if method == "initialize" {
		context = diagnosticInitializeLifecycle
	} else if method == "session/new" {
		context = diagnosticSessionLifecycle
	}
	return withDiagnosticContext(fmt.Errorf("%w: %s lifecycle: %w", ErrIncompatible, safeMethod(method), cause), context)
}

// preflightStatusFromMeta recognizes the narrow optional status keys used by
// compatible agents: toolInventory and startupRoot. The local launcher proof
// above is mandatory; wire status is not. Once either key is announced its
// contents become authoritative and malformed or contradictory evidence fails
// closed. This does not introduce an additionalDirectories dependency.
func preflightStatusFromMeta(expectedStartupRoot string, values ...json.RawMessage) ([]string, bool, error) {
	var found []string
	present := false
	for _, raw := range values {
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, false, withDiagnosticContext(fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible), diagnosticToolInventory)
		}
		if startupRaw, ok := meta["startupRoot"]; ok {
			var startupRoot string
			if json.Unmarshal(startupRaw, &startupRoot) != nil || startupRoot == "" {
				return nil, false, withDiagnosticContext(fmt.Errorf("%w: malformed startup-root status", ErrIncompatible), diagnosticStartupRoot)
			}
			if startupRoot != expectedStartupRoot {
				return nil, false, withDiagnosticContext(fmt.Errorf("%w: contradictory startup-root status", ErrIncompatible), diagnosticStartupRoot)
			}
		}
		inventoryRaw, ok := meta["toolInventory"]
		if !ok {
			continue
		}
		var inventory []string
		decoder := json.NewDecoder(bytes.NewReader(inventoryRaw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&inventory); err != nil {
			return nil, false, withDiagnosticContext(fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible), diagnosticToolInventory)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, false, withDiagnosticContext(fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible), diagnosticToolInventory)
		}
		sort.Strings(inventory)
		if present && !equalStrings(found, inventory) {
			return nil, false, withDiagnosticContext(fmt.Errorf("%w: contradictory tool inventory status", ErrIncompatible), diagnosticToolInventory)
		}
		found, present = inventory, true
	}
	return found, present, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
