package specflow

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestLineChatWritesOnePromptPerMessage(t *testing.T) {
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("brief\n")), output: &output}
	value, err := ui.text()
	if err != nil || value != "brief" {
		t.Fatalf("input = %q, %v", value, err)
	}
	ui.say("question")
	ui.thinking()

	for _, want := range []string{"Вы > ", "Stepan > question", "Stepan думает…"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("chat output = %q, missing %q", output.String(), want)
		}
	}
	if strings.Count(output.String(), "question") != 1 {
		t.Fatalf("chat duplicated assistant message: %q", output.String())
	}
}
