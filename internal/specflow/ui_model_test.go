package specflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"charm.land/huh/v2"
)

func TestParseMainCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  MainCommand
		valid bool
	}{
		{"idea", "/idea", MainCommand{Action: MainActionIdea, NeedBrief: true}, true},
		{"empty remainder", "/idea   ", MainCommand{Action: MainActionIdea, Brief: "  ", NeedBrief: true}, true},
		{"literal", `/idea "quoted" \\ path`, MainCommand{Action: MainActionIdea, Brief: `"quoted" \\ path`}, true},
		{"unknown", "/other", MainCommand{}, false},
		{"not an alias", "idea text", MainCommand{}, false},
		{"not a prefix", "/idea-more", MainCommand{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseMainCommand(test.input)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatalf("parsed unknown command as %#v", got)
			}
			if test.valid && got != test.want {
				t.Fatalf("command = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestDraftActionModel(t *testing.T) {
	want := []DraftAction{DraftApprove, DraftQuestion, DraftChange}
	if got := DraftActions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %#v, want %#v", got, want)
	}
	if DraftApprove.NeedsText() || !DraftQuestion.NeedsText() || !DraftChange.NeedsText() {
		t.Fatal("draft text input routing is wrong")
	}
}

func TestUIAccessibilityUsesEnvironmentPresence(t *testing.T) {
	t.Setenv("ACCESSIBLE", "")
	if ui := NewUI(nil); !ui.accessible {
		t.Fatal("ACCESSIBLE presence did not enable accessible mode")
	}
}

func TestDraftTitleAlwaysDisplaysEntrypoint(t *testing.T) {
	progress := Progress{Path: "docs/specs/example/specification.md", Answer: "answer"}
	if title := draftTitle(progress); !strings.Contains(title, progress.Path) {
		t.Fatalf("draft title %q does not display %q", title, progress.Path)
	}
}

func TestHuhCancelMapsToSingleSentinel(t *testing.T) {
	// Normal mode cancellation is covered by huh; keep our boundary mapping
	// independently testable without terminal rendering.
	if err := normalizeFormError(huh.ErrUserAborted); !errors.Is(err, ErrCanceled) {
		t.Fatalf("cancel error = %v", err)
	}
}
