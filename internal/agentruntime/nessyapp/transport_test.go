package nessyapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestTransportKeepsStringAndIntegerIDsDistinct(t *testing.T) {
	var output bytes.Buffer
	client := newTransport(strings.NewReader(
		`{"jsonrpc":"2.0","id":"1","result":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":1,"result":{}}`+"\n",
	), &output)
	if err := client.sendRequest(stringID("1"), "first", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := client.sendRequest(integerID(1), "second", struct{}{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"s:1", "i:1"} {
		got, err := client.read()
		if err != nil {
			t.Fatal(err)
		}
		if got.id.key != want {
			t.Fatalf("response id = %q, want %q", got.id.key, want)
		}
	}
	decoder := newLineDecoder(&output)
	for range 2 {
		line, err := decoder.readLine()
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil || string(envelope["jsonrpc"]) != `"2.0"` {
			t.Fatalf("invalid outgoing envelope: %s (%v)", line, err)
		}
	}
}

func TestTransportRejectsMalformedAndAmbiguousFrames(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{name: "invalid UTF-8", input: append([]byte(`{"jsonrpc":"2.0","method":"x","params":{"value":"`), 0xff, '"', '}', '}', '\n'), want: errInvalidNDJSON},
		{name: "empty line", input: []byte("\n"), want: errInvalidNDJSON},
		{name: "truncated line", input: []byte(`{"jsonrpc":"2.0"}`), want: errTruncatedNDJSON},
		{name: "trailing JSON", input: []byte("{\"jsonrpc\":\"2.0\"} {}\n"), want: errInvalidNDJSON},
		{name: "duplicate nested key", input: []byte("{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"a\":1,\"a\":2}}\n"), want: errInvalidNDJSON},
		{name: "missing jsonrpc", input: []byte("{\"method\":\"x\",\"params\":{}}\n"), want: errInvalidEnvelope},
		{name: "fractional ID", input: []byte("{\"jsonrpc\":\"2.0\",\"id\":1.5,\"result\":{}}\n"), want: errInvalidEnvelope},
		{name: "null ID", input: []byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"result\":{}}\n"), want: errInvalidEnvelope},
		{name: "unknown envelope member", input: []byte("{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{},\"secret\":\"do-not-echo\"}\n"), want: errInvalidEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newTransport(bytes.NewReader(test.input), io.Discard).read()
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("wire payload leaked: %v", err)
			}
		})
	}
}

func TestTransportBoundsFrames(t *testing.T) {
	input := append(bytes.Repeat([]byte{'x'}, maxACPMessageBytes+1), '\n')
	if _, err := newTransport(bytes.NewReader(input), io.Discard).read(); !errors.Is(err, errNDJSONLineTooLong) {
		t.Fatalf("read error = %v", err)
	}
	if err := (&lineWriter{writer: io.Discard}).write(strings.Repeat("x", maxACPMessageBytes)); !errors.Is(err, errNDJSONLineTooLong) {
		t.Fatalf("write error = %v", err)
	}
}

func TestTransportSeparatesLocalAndRemoteCorrelation(t *testing.T) {
	lines := `{"jsonrpc":"2.0","id":"same","method":"session/request_permission","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":"same","result":{}}` + "\n"
	transport := newTransport(strings.NewReader(lines), io.Discard)
	if err := transport.sendRequest(stringID("same"), "local", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got, err := transport.read(); err != nil || got.kind != requestMessage {
		t.Fatalf("remote request = %+v, %v", got, err)
	}
	if got, err := transport.read(); err != nil || got.kind != responseMessage {
		t.Fatalf("local response = %+v, %v", got, err)
	}
}

func TestTransportRejectsDuplicateAndOrphanCorrelation(t *testing.T) {
	t.Run("duplicate remote request", func(t *testing.T) {
		line := `{"jsonrpc":"2.0","id":1,"method":"x","params":{}}` + "\n"
		transport := newTransport(strings.NewReader(line+line), io.Discard)
		if _, err := transport.read(); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.read(); !errors.Is(err, errDuplicateRequestID) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("orphan response", func(t *testing.T) {
		transport := newTransport(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}`+"\n"), io.Discard)
		if _, err := transport.read(); !errors.Is(err, errOrphanResponse) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("duplicate response", func(t *testing.T) {
		line := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
		transport := newTransport(strings.NewReader(line+line), io.Discard)
		if err := transport.sendRequest(integerID(1), "x", struct{}{}); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.read(); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.read(); !errors.Is(err, errDuplicateResponse) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestTransportBoundsCorrelationIdentityAndHistory(t *testing.T) {
	oversized := strings.Repeat("x", maxCorrelationIDBytes+1)
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": oversized, "result": map[string]any{}})
	line = append(line, '\n')
	if _, err := newTransport(bytes.NewReader(line), io.Discard).read(); !errors.Is(err, errRequestIDTooLong) {
		t.Fatalf("oversized inbound ID = %v", err)
	}
	if err := newTransport(strings.NewReader(""), io.Discard).sendRequest(stringID(oversized), "x", struct{}{}); !errors.Is(err, errRequestIDTooLong) {
		t.Fatalf("oversized outbound ID = %v", err)
	}

	var table correlationTable
	for index := range maxOutstandingACPCalls {
		if err := table.register(integerID(int64(index + 1))); err != nil {
			t.Fatal(err)
		}
	}
	if err := table.register(integerID(maxOutstandingACPCalls + 1)); !errors.Is(err, errTooManyRequests) {
		t.Fatalf("outstanding flood = %v", err)
	}
	for index := range maxOutstandingACPCalls {
		if err := table.resolve(integerID(int64(index + 1))); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < maxCorrelationTombstones+1; index++ {
		id := integerID(int64(10_000 + index))
		if err := table.register(id); err != nil {
			t.Fatal(err)
		}
		if err := table.resolve(id); err != nil {
			t.Fatal(err)
		}
	}
	if len(table.pending) != 0 || len(table.resolved) != maxCorrelationTombstones || len(table.resolvedOrder) != maxCorrelationTombstones {
		t.Fatalf("bounded table sizes pending=%d resolved=%d order=%d", len(table.pending), len(table.resolved), len(table.resolvedOrder))
	}
	if err := table.resolve(integerID(int64(10_000 + maxCorrelationTombstones))); !errors.Is(err, errDuplicateResponse) {
		t.Fatalf("recent replay classification = %v", err)
	}
	if err := table.resolve(integerID(10_000)); !errors.Is(err, errOrphanResponse) {
		t.Fatalf("evicted replay classification = %v", err)
	}
}

func TestLineWriterSerializesConcurrentMessages(t *testing.T) {
	var output bytes.Buffer
	writer := &lineWriter{writer: &output}
	var group sync.WaitGroup
	for index := range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := writer.write(map[string]int{"index": index}); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	decoder := newLineDecoder(&output)
	seen := make(map[int]bool)
	for {
		line, err := decoder.readLine()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]int
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		seen[value["index"]] = true
	}
	if len(seen) != 100 {
		t.Fatalf("message count = %d", len(seen))
	}
}
