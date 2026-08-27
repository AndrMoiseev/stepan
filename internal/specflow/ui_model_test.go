package specflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseMainCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  MainCommand
		valid bool
	}{
		{"feature", "/feature", MainCommand{Action: MainActionFeature, NeedBrief: true}, true},
		{"empty remainder", "/feature   ", MainCommand{Action: MainActionFeature, Brief: "  ", NeedBrief: true}, true},
		{"literal", `/feature "quoted" \\ path`, MainCommand{Action: MainActionFeature, Brief: `"quoted" \\ path`}, true},
		{"renamed command", "/idea", MainCommand{}, false},
		{"unknown", "/other", MainCommand{}, false},
		{"not an alias", "feature text", MainCommand{}, false},
		{"not a prefix", "/feature-more", MainCommand{}, false},
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
	for input, want := range map[string]DraftAction{"/approve": DraftApprove, "/question": DraftQuestion, "change": DraftChange} {
		if got, err := ParseDraftAction(input); err != nil || got != want {
			t.Fatalf("action %q = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseDraftAction("/other"); err == nil {
		t.Fatal("accepted unknown draft action")
	}
}

func TestDraftTitleAlwaysDisplaysEntrypoint(t *testing.T) {
	progress := Progress{Path: "docs/changes/features/example/specification.md", Answer: "answer"}
	if title := draftTitle(progress); !strings.Contains(title, progress.Path) {
		t.Fatalf("draft title %q does not display %q", title, progress.Path)
	}
}
