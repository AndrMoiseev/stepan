package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var ErrConnectionClosed = errors.New("JSON-RPC connection closed")

// Handler receives server messages. Request handlers may respond asynchronously;
// unregistered methods are rejected as unsupported.
type Handler struct {
	Notification func(Message) error
	Requests     map[string]func(*Connection, Message) error
}

type Platform struct {
	CodexHome      string `json:"codex_home"`
	PlatformFamily string `json:"platform_family"`
	PlatformOS     string `json:"platform_os"`
	UserAgent      string `json:"user_agent"`
}

type callResult struct {
	message Message
}

// Connection dispatches one JSON-RPC stream. NewConnection performs the only
// initialize/initialized handshake for the stream before returning.
type Connection struct {
	transport *Transport
	handler   Handler
	platform  Platform

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan callResult
	err     error
	done    chan struct{}
	turn    *turnRun
	turnMu  sync.Mutex
}

func NewConnection(transport *Transport, handler Handler) (*Connection, error) {
	connection := newConnection(transport, handler)
	var response struct {
		CodexHome      string `json:"codexHome"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
		UserAgent      string `json:"userAgent"`
	}
	if err := connection.Call("initialize", map[string]any{
		"clientInfo": map[string]string{"name": "stepan", "version": "0"},
	}, &response); err != nil {
		connection.fail(err)
		return nil, err
	}
	if response.CodexHome == "" || response.PlatformFamily == "" || response.PlatformOS == "" || response.UserAgent == "" {
		err := errors.New("invalid initialize response")
		connection.fail(err)
		return nil, err
	}
	connection.platform = Platform(response)
	if err := connection.transport.SendNotification("initialized", struct{}{}); err != nil {
		connection.fail(err)
		return nil, err
	}
	return connection, nil
}

func newConnection(transport *Transport, handler Handler) *Connection {
	connection := &Connection{
		transport: transport,
		handler:   handler,
		nextID:    1,
		pending:   make(map[string]chan callResult),
		done:      make(chan struct{}),
	}
	go connection.read()
	return connection
}

func (connection *Connection) Platform() Platform { return connection.platform }

func (connection *Connection) Done() <-chan struct{} { return connection.done }

func (connection *Connection) Err() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.err
}

// Call assigns the request ID, waits for its response, and decodes its result.
func (connection *Connection) Call(method string, params, result any) error {
	connection.mu.Lock()
	if connection.err != nil {
		err := connection.err
		connection.mu.Unlock()
		return err
	}
	id := IntID(connection.nextID)
	connection.nextID++
	response := make(chan callResult, 1)
	connection.pending[id.Key()] = response
	connection.mu.Unlock()

	if err := connection.transport.SendRequest(id, method, params); err != nil {
		connection.fail(err)
		return connection.Err()
	}

	select {
	case received := <-response:
		if received.message.Error != nil {
			return received.message.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(received.message.Result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-connection.done:
		return connection.Err()
	}
}

func (connection *Connection) Respond(id ID, result any) error {
	if err := connection.Err(); err != nil {
		return err
	}
	if err := connection.transport.SendResult(id, result); err != nil {
		connection.fail(err)
		return connection.Err()
	}
	return nil
}

func (connection *Connection) Reject(id ID, rpcError RPCError) error {
	if err := connection.Err(); err != nil {
		return err
	}
	if err := connection.transport.SendError(id, rpcError); err != nil {
		connection.fail(err)
		return connection.Err()
	}
	return nil
}

func (connection *Connection) Close() error {
	connection.fail(ErrConnectionClosed)
	return nil
}

func (connection *Connection) read() {
	for {
		message, err := connection.transport.Read()
		if err != nil {
			connection.fail(err)
			return
		}
		switch message.Kind {
		case Response:
			connection.mu.Lock()
			response := connection.pending[message.ID.Key()]
			delete(connection.pending, message.ID.Key())
			connection.mu.Unlock()
			if response == nil {
				connection.fail(ErrOrphanResponse)
				return
			}
			response <- callResult{message: message}
		case Notification:
			if err := connection.dispatchTurnMessage(message); err != nil {
				connection.fail(err)
				return
			}
			if connection.handler.Notification != nil {
				if err := connection.handler.Notification(message); err != nil {
					connection.fail(err)
					return
				}
			}
		case Request:
			if strings.HasPrefix(message.Method, "item/") {
				if err := connection.validateTurnMessage(message); err != nil {
					connection.fail(err)
					return
				}
			}
			if IsApprovalMethod(message.Method) {
				handle, err := connection.registerTurnApproval(message)
				if err != nil {
					connection.fail(err)
					return
				}
				go func() {
					if err := handle(); err != nil {
						connection.fail(err)
					}
				}()
				continue
			}
			handle := connection.handler.Requests[message.Method]
			if handle == nil {
				if err := connection.Reject(message.ID, RPCError{Code: -32601, Message: "method not found"}); err != nil {
					return
				}
				continue
			}
			go func() {
				if err := handle(connection, message); err != nil {
					connection.fail(err)
				}
			}()
		}
	}
}

func (connection *Connection) fail(cause error) {
	connection.mu.Lock()
	if connection.err != nil {
		connection.mu.Unlock()
		return
	}
	if errors.Is(cause, ErrConnectionClosed) {
		connection.err = cause
	} else {
		connection.err = closedError{cause}
	}
	connection.pending = nil
	close(connection.done)
	run := connection.turn
	connection.mu.Unlock()
	if run != nil {
		run.clearPending()
	}
}

type closedError struct{ cause error }

func (err closedError) Error() string   { return fmt.Sprintf("%s: %v", ErrConnectionClosed, err.cause) }
func (err closedError) Unwrap() []error { return []error{ErrConnectionClosed, err.cause} }

func (rpcError *RPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", rpcError.Code, rpcError.Message)
}
