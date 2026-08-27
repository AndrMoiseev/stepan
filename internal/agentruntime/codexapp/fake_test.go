package codexapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMain(main *testing.M) {
	if scenario := os.Getenv("GO_WANT_CODEXAPP_FAKE"); scenario != "" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			version := SupportedCodexVersion
			if scenario == "version-mismatch" {
				version = "0.148.0"
			}
			fmt.Println("codex-cli", version)
			os.Exit(0)
		}
		os.Exit(runFake(scenario))
	}
	os.Exit(main.Run())
}

func runFake(scenario string) int {
	switch scenario {
	case "correlation":
		transport := NewTransport(os.Stdin, os.Stdout)
		first, err := transport.Read()
		if err != nil {
			return 10
		}
		second, err := transport.Read()
		if err != nil {
			return 11
		}
		if err := transport.SendNotification("future/notification", map[string]bool{"preserved": true}); err != nil {
			return 12
		}
		if err := transport.SendResult(second.ID, map[string]string{"echo": second.ID.Key()}); err != nil {
			return 13
		}
		if err := transport.SendResult(first.ID, map[string]string{"echo": first.ID.Key()}); err != nil {
			return 14
		}
		return 0
	case "invalid":
		fmt.Fprintln(os.Stdout, `{"id":`)
		return 0
	case "truncated":
		fmt.Fprint(os.Stdout, `{"id":1`)
		return 0
	case "oversized":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, MaxMessageBytes+1))
		fmt.Fprintln(os.Stdout)
		return 0
	case "process-echo":
		if strings.Join(os.Args[1:], "\x00") != strings.Join(appServerArgs, "\x00") {
			return 30
		}
		var line string
		if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
			return 31
		}
		fmt.Fprintln(os.Stdout, line)
		fmt.Fprint(os.Stderr, strings.Repeat("e", maxDiagnosticBytes+1024))
		return 0
	case "early-exit":
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "early exit")
		return 17
	case "process-tree":
		return runProcessTreeFake()
	case "runtime-interrupt-ack", "runtime-interrupt-ignore", "runtime-crash", "runtime-restart":
		return runRuntimeFake(scenario)
	default:
		return 9
	}
}

func runRuntimeFake(scenario string) int {
	if os.Getenv("STEPAN_CODEXAPP_TREE_LEVEL") != "" {
		return runProcessTreeFake()
	}
	if pidFile := os.Getenv("STEPAN_CODEXAPP_PID_FILE"); pidFile != "" {
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_CODEXAPP_TREE_LEVEL=child")
		if err := command.Start(); err != nil {
			return 60
		}
	}
	transport := NewTransport(os.Stdin, os.Stdout)
	initialize, err := transport.Read()
	if err != nil || initialize.Method != "initialize" {
		return 61
	}
	if err := transport.SendResult(initialize.ID, map[string]string{
		"codexHome": "fake", "platformFamily": "windows", "platformOs": "windows", "userAgent": "fake/1",
	}); err != nil {
		return 62
	}
	initialized, err := transport.Read()
	if err != nil || initialized.Kind != Notification || initialized.Method != "initialized" {
		return 63
	}
	threadRequest, err := transport.Read()
	if err != nil || threadRequest.Method != "thread/start" {
		return 64
	}
	threadID := "thread-old"
	if scenario == "runtime-restart" {
		threadID = "thread-new"
	}
	if err := transport.SendResult(threadRequest.ID, map[string]any{"thread": map[string]string{"id": threadID}}); err != nil {
		return 65
	}
	turnRequest, err := transport.Read()
	if err != nil || turnRequest.Method != "turn/start" {
		return 66
	}
	if err := transport.SendResult(turnRequest.ID, map[string]any{"turn": map[string]string{"id": "turn-1"}}); err != nil {
		return 67
	}
	if scenario == "runtime-crash" {
		fmt.Fprintln(os.Stderr, "fake crash")
		return 17
	}
	interrupt, err := transport.Read()
	if err != nil || interrupt.Method != "turn/interrupt" {
		return 68
	}
	var ids struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(interrupt.Params, &ids) != nil || ids.ThreadID != threadID || ids.TurnID != "turn-1" {
		return 69
	}
	if scenario == "runtime-interrupt-ignore" {
		time.Sleep(30 * time.Second)
		return 70
	}
	if err := transport.SendResult(interrupt.ID, struct{}{}); err != nil {
		return 71
	}
	if err := transport.SendNotification("turn/completed", map[string]any{
		"threadId": threadID, "turn": map[string]any{"id": "turn-1", "status": "interrupted", "items": []any{}},
	}); err != nil {
		return 72
	}
	return 0
}

func runProcessTreeFake() int {
	switch os.Getenv("STEPAN_CODEXAPP_TREE_LEVEL") {
	case "":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_CODEXAPP_TREE_LEVEL=child")
		if err := command.Start(); err != nil {
			return 50
		}
		time.Sleep(30 * time.Second)
	case "child":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_CODEXAPP_TREE_LEVEL=grandchild")
		if err := command.Start(); err != nil {
			return 51
		}
		data, err := json.Marshal([]int{os.Getppid(), os.Getpid(), command.Process.Pid})
		if err != nil || os.WriteFile(os.Getenv("STEPAN_CODEXAPP_PID_FILE"), data, 0o600) != nil {
			return 52
		}
		time.Sleep(30 * time.Second)
	case "grandchild":
		time.Sleep(30 * time.Second)
	default:
		return 53
	}
	return 0
}
