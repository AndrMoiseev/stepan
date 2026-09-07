package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestPrepareAgentLoginRunsOnlyForSelectedNessyQwen(t *testing.T) {
	loginFailure := errors.New("login failed")
	tests := []struct {
		name      string
		config    agentConfig
		wantCalls int
		wantError bool
	}{
		{name: "selected Nessy Qwen", config: agentConfig{kind: agentQwen, executable: "nessy"}, wantCalls: 1, wantError: true},
		{name: "official Qwen", config: agentConfig{kind: agentQwen, executable: "qwen"}},
		{name: "other compatible Qwen", config: agentConfig{kind: agentQwen, executable: "corporate-qwen"}},
		{name: "non-Qwen provider", config: agentConfig{kind: agentClaude, executable: "nessy"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := bytes.NewBufferString("operator input")
			output := &bytes.Buffer{}
			errorOutput := &bytes.Buffer{}
			calls := 0
			login := func(ctx context.Context, executable, workspace string, stdin io.Reader, stdout, stderr io.Writer) error {
				calls++
				if ctx != context.Background() || executable != "nessy" || workspace != `C:\workspace` || stdin != input || stdout != output || stderr != errorOutput {
					t.Fatalf("interactive login inputs = %v, %q, %q, %T, %T, %T", ctx, executable, workspace, stdin, stdout, stderr)
				}
				return loginFailure
			}

			err := prepareAgentLogin(context.Background(), test.config, `C:\workspace`, input, output, errorOutput, login)
			if calls != test.wantCalls {
				t.Fatalf("login calls = %d, want %d", calls, test.wantCalls)
			}
			if test.wantError {
				if !errors.Is(err, loginFailure) {
					t.Fatalf("login error = %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected login error = %v", err)
			}
		})
	}
}
