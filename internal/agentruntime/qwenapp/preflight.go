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
	if process == nil || process.Stdin() == nil || process.Stdout() == nil || process.WorkspaceRoot() == "" {
		if process != nil {
			_ = process.Close()
		}
		return nil, fmt.Errorf("%w: contained process is not started", ErrIncompatible)
	}
	connection := newConnection(newTransport(process.Stdout(), process.Stdin()), process, connectionHandler{})
	if err := connection.preflight(process.WorkspaceRoot()); err != nil {
		connection.fail(err)
		return nil, connection.Err()
	}
	return connection, nil
}

func (connection *Connection) preflight(workspace string) error {
	var initialized initializeResponse
	if err := connection.callAndCommit("initialize", initializeParams{
		ProtocolVersion:    acpProtocolVersion,
		ClientCapabilities: clientCapabilities{},
		ClientInfo: implementationInfo{
			Name: "stepan", Title: "Stepan", Version: "0",
		},
	}, &initialized, func() error {
		if initialized.ProtocolVersion != acpProtocolVersion {
			return fmt.Errorf("%w: initialize protocolVersion", ErrIncompatible)
		}
		// ACP v1 defines session/new, session/prompt, session/cancel,
		// session/update, and session/request_permission as baseline methods,
		// not capability flags. Protocol-version agreement declares that
		// baseline; the non-nil capabilities object proves the mandatory
		// initialize shape, and session/new is exercised below. Optional
		// content and additional-directory flags do not gate compatibility.
		if initialized.AgentCapabilities == nil {
			return fmt.Errorf("%w: initialize agentCapabilities", ErrIncompatible)
		}
		if inventory, present, err := toolInventoryFromMeta(initialized.Meta, initialized.AgentCapabilities.Meta); err != nil {
			return err
		} else if present {
			if err := validateToolInventory(inventory); err != nil {
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
		if session.SessionID == "" {
			return fmt.Errorf("%w: session/new sessionId", ErrIncompatible)
		}
		if inventory, present, err := toolInventoryFromMeta(session.Meta); err != nil {
			return err
		} else if present {
			if err := validateToolInventory(inventory); err != nil {
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

func incompatibleCall(method string, cause error) error {
	if errors.Is(cause, ErrConnectionClosed) {
		return cause
	}
	if errors.Is(cause, ErrIncompatible) {
		return cause
	}
	return fmt.Errorf("%w: %s lifecycle", ErrIncompatible, safeMethod(method))
}

// toolInventoryFromMeta recognizes the narrow status extension used by fake
// and compatible agents: {"toolInventory":["name", ...]}. Absence is allowed;
// malformed or contradictory occurrences fail closed.
func toolInventoryFromMeta(values ...json.RawMessage) ([]string, bool, error) {
	var found []string
	present := false
	for _, raw := range values {
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, false, fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible)
		}
		inventoryRaw, ok := meta["toolInventory"]
		if !ok {
			continue
		}
		var inventory []string
		decoder := json.NewDecoder(bytes.NewReader(inventoryRaw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&inventory); err != nil {
			return nil, false, fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, false, fmt.Errorf("%w: malformed tool inventory status", ErrIncompatible)
		}
		sort.Strings(inventory)
		if present && !equalStrings(found, inventory) {
			return nil, false, fmt.Errorf("%w: contradictory tool inventory status", ErrIncompatible)
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
