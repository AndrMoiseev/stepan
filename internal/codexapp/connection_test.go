package codexapp

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestConnectionHandshakesOnceAndDispatchesRequests(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	server := NewTransport(serverSide, serverSide)
	serverErr := make(chan error, 1)
	go func() {
		initialize, err := server.Read()
		if err != nil || initialize.Method != "initialize" || initialize.ID.Key() != IntID(1).Key() {
			serverErr <- errors.New("invalid initialize request")
			return
		}
		if err := server.SendResult(initialize.ID, map[string]string{
			"codexHome": "test", "platformFamily": "windows", "platformOs": "windows", "userAgent": "test/1",
		}); err != nil {
			serverErr <- err
			return
		}
		initialized, err := server.Read()
		if err != nil || initialized.Kind != Notification || initialized.Method != "initialized" {
			serverErr <- errors.New("invalid initialized notification")
			return
		}
		for id, method := range []string{"first", "second"} {
			request, err := server.Read()
			if err != nil || request.Method != method || request.ID.Key() != IntID(int64(id+2)).Key() {
				serverErr <- errors.New("invalid client request")
				return
			}
			if err := server.SendResult(request.ID, map[string]string{"value": method}); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	connection, err := NewConnection(NewTransport(clientSide, clientSide), Handler{})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Platform().UserAgent != "test/1" {
		t.Fatalf("platform = %+v", connection.Platform())
	}
	for _, method := range []string{"first", "second"} {
		var response struct {
			Value string `json:"value"`
		}
		if err := connection.Call(method, nil, &response); err != nil || response.Value != method {
			t.Fatalf("%s response = %+v, %v", method, response, err)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestConnectionRequestHandlerDoesNotBlockReading(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	release := make(chan struct{})
	notified := make(chan struct{})
	connection := newConnection(NewTransport(clientSide, clientSide), Handler{
		Requests: map[string]func(*Connection, Message) error{
			"approval": func(connection *Connection, message Message) error {
				<-release
				return connection.Respond(message.ID, struct{}{})
			},
		},
		Notification: func(message Message) error {
			close(notified)
			return nil
		},
	})
	server := NewTransport(serverSide, serverSide)
	if err := server.SendRequest(StringID("approval"), "approval", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := server.SendNotification("progress", struct{}{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("notification was blocked by server request")
	}
	close(release)
	response, err := server.Read()
	if err != nil || response.ID.Key() != StringID("approval").Key() {
		t.Fatalf("approval response = %+v, %v", response, err)
	}
	_ = connection.Close()
}

func TestConnectionRejectsUnknownServerRequest(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	connection := newConnection(NewTransport(clientSide, clientSide), Handler{})
	server := NewTransport(serverSide, serverSide)
	if err := server.SendRequest(IntID(9), "unknown", struct{}{}); err != nil {
		t.Fatal(err)
	}
	response, err := server.Read()
	if err != nil || response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("unknown response = %+v, %v", response, err)
	}
	_ = connection.Close()
}

func TestConnectionFailsClosedOnBadResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(*Transport, net.Conn) error
		want error
	}{
		{"orphan", func(server *Transport, raw net.Conn) error {
			_, err := io.WriteString(raw, "{\"id\":99,\"result\":{}}\n")
			return err
		}, ErrOrphanResponse},
		{"duplicate", func(server *Transport, raw net.Conn) error {
			if _, err := io.WriteString(raw, "{\"id\":1,\"result\":{}}\n"); err != nil {
				return err
			}
			_, err := io.WriteString(raw, "{\"id\":1,\"result\":{}}\n")
			return err
		}, ErrDuplicateResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientSide, serverSide := net.Pipe()
			defer clientSide.Close()
			defer serverSide.Close()
			connection := newConnection(NewTransport(clientSide, clientSide), Handler{})
			if test.name == "duplicate" {
				connection.mu.Lock()
				connection.pending[IntID(1).Key()] = make(chan callResult, 1)
				connection.mu.Unlock()
				if err := connection.transport.correlation.register(&connection.transport.correlation.local, IntID(1)); err != nil {
					t.Fatal(err)
				}
			}
			if err := test.send(NewTransport(serverSide, serverSide), serverSide); err != nil {
				t.Fatal(err)
			}
			select {
			case <-connection.Done():
			case <-time.After(time.Second):
				t.Fatal("connection remained open")
			}
			if !errors.Is(connection.Err(), ErrConnectionClosed) || !errors.Is(connection.Err(), test.want) {
				t.Fatalf("connection error = %v", connection.Err())
			}
		})
	}
}

func TestConnectionExitUnblocksAllCalls(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	connection := newConnection(NewTransport(clientSide, clientSide), Handler{})
	var wait sync.WaitGroup
	errorsOut := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsOut <- connection.Call("wait", nil, nil)
		}()
	}
	server := NewTransport(serverSide, serverSide)
	for range 2 {
		if _, err := server.Read(); err != nil {
			t.Fatal(err)
		}
	}
	_ = serverSide.Close()
	wait.Wait()
	close(errorsOut)
	var first error
	for err := range errorsOut {
		if !errors.Is(err, ErrConnectionClosed) || !errors.Is(err, io.EOF) {
			t.Fatalf("pending call error = %v", err)
		}
		if first == nil {
			first = err
		} else if err.Error() != first.Error() {
			t.Fatalf("different close errors: %v / %v", first, err)
		}
	}
	if err := connection.Call("after-exit", nil, nil); err == nil || err.Error() != first.Error() {
		t.Fatalf("call after exit = %v", err)
	}
}

func TestConnectionCloseUnblocksPendingCall(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	connection := newConnection(NewTransport(clientSide, clientSide), Handler{})
	callErr := make(chan error, 1)
	go func() { callErr <- connection.Call("wait", nil, nil) }()
	if _, err := NewTransport(serverSide, serverSide).Read(); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-callErr:
		if !errors.Is(err, ErrConnectionClosed) {
			t.Fatalf("pending call error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call remained blocked")
	}
}
