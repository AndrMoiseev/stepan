package codexapp

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestMain(main *testing.M) {
	if scenario := os.Getenv("GO_WANT_CODEXAPP_FAKE"); scenario != "" {
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
	default:
		return 9
	}
}
