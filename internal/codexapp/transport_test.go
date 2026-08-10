package codexapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestTransportCorrelatesFakeResponses(t *testing.T) {
	command, stdin, stdout := startFake(t, "correlation")
	transport := NewTransport(stdout, stdin)

	ids := []ID{StringID("1"), IntID(1)}
	var group sync.WaitGroup
	errorsOut := make(chan error, len(ids))
	for _, id := range ids {
		id := id
		group.Add(1)
		go func() {
			defer group.Done()
			errorsOut <- transport.SendRequest(id, "echo", map[string]string{"id": id.Key()})
		}()
	}
	group.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}

	seen := make(map[string]bool)
	for range 3 {
		message, err := transport.Read()
		if err != nil {
			t.Fatal(err)
		}
		if message.Kind == Notification {
			if message.Method != "future/notification" || len(message.Raw) == 0 {
				t.Fatalf("notification = %+v", message)
			}
			continue
		}
		if message.Kind != Response {
			t.Fatalf("kind = %v", message.Kind)
		}
		seen[message.ID.Key()] = true
	}
	if !seen[StringID("1").Key()] || !seen[IntID(1).Key()] {
		t.Fatalf("response IDs = %v", seen)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportRejectsBadCorrelation(t *testing.T) {
	t.Run("orphan response", func(t *testing.T) {
		transport := NewTransport(strings.NewReader("{\"id\":1,\"result\":{}}\n"), io.Discard)
		if _, err := transport.Read(); !errors.Is(err, ErrOrphanResponse) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate response", func(t *testing.T) {
		transport := NewTransport(strings.NewReader("{\"id\":1,\"result\":{}}\n{\"id\":1,\"result\":{}}\n"), io.Discard)
		if err := transport.SendRequest(IntID(1), "echo", struct{}{}); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.Read(); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.Read(); !errors.Is(err, ErrDuplicateResponse) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate remote request", func(t *testing.T) {
		lines := "{\"method\":\"ask\",\"params\":{},\"id\":\"x\"}\n{\"method\":\"ask\",\"params\":{},\"id\":\"x\"}\n"
		transport := NewTransport(strings.NewReader(lines), io.Discard)
		if _, err := transport.Read(); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.Read(); !errors.Is(err, ErrDuplicateRequestID) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestTransportReadsErrorResponse(t *testing.T) {
	transport := NewTransport(strings.NewReader("{\"id\":\"request\",\"error\":{\"code\":-32600,\"message\":\"bad request\",\"data\":{\"retry\":false}}}\n"), io.Discard)
	if err := transport.SendRequest(StringID("request"), "echo", struct{}{}); err != nil {
		t.Fatal(err)
	}
	message, err := transport.Read()
	if err != nil {
		t.Fatal(err)
	}
	if message.Error == nil || message.Error.Code != -32600 || message.Error.Message != "bad request" || string(message.Error.Data) != `{"retry":false}` {
		t.Fatalf("error response = %+v", message.Error)
	}
}

func TestFakeProducesProtocolFailures(t *testing.T) {
	tests := []struct {
		scenario string
		want     error
	}{
		{"invalid", ErrInvalidJSONL},
		{"truncated", ErrTruncatedJSONL},
		{"oversized", ErrJSONLLineTooLong},
	}
	for _, test := range tests {
		t.Run(test.scenario, func(t *testing.T) {
			command, stdin, stdout := startFake(t, test.scenario)
			_ = stdin.Close()
			_, err := NewDecoder(stdout).Decode()
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err := command.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWriterSerializesConcurrentMessages(t *testing.T) {
	var output bytes.Buffer
	writer := NewWriter(&output)
	var group sync.WaitGroup
	for index := range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := writer.Write(map[string]int{"index": index}); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()

	decoder := NewDecoder(&output)
	seen := make(map[int]bool)
	for {
		message := make(map[string]int)
		line, err := decoder.readLine()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
		seen[message["index"]] = true
	}
	if len(seen) != 100 {
		t.Fatalf("messages = %d", len(seen))
	}
}

func startFake(t *testing.T, scenario string) (*exec.Cmd, io.WriteCloser, io.ReadCloser) {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), "GO_WANT_CODEXAPP_FAKE="+scenario)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return command, stdin, stdout
}
