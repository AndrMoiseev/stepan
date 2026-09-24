//go:build process_integration

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestRunPreservesExitAndEnforcesDeadline(t *testing.T) {
	for _, test := range []struct {
		mode string
		code int
	}{{"success", 0}, {"failure", 7}, {"deadline", 1}} {
		t.Run(test.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			code, err := run(ctx, os.Args[0], []string{"-test.run=^TestSuiteProcessHelper$", "--", test.mode}, io.Discard, io.Discard)
			if code != test.code {
				t.Fatalf("exit code = %d, error = %v", code, err)
			}
			if test.mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline error = %v", err)
			}
			if test.mode == "success" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSuiteProcessHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		t.Skip("subprocess helper")
	}
	switch os.Args[len(os.Args)-1] {
	case "success":
		os.Exit(0)
	case "failure":
		os.Exit(7)
	case "deadline":
		time.Sleep(time.Hour)
	default:
		t.Fatal("unknown helper mode")
	}
}
