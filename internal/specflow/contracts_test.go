package specflow

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestResultDecoders(t *testing.T) {
	tests := []struct {
		name   string
		decode func([]byte) (Result, error)
		input  string
		want   Result
		valid  bool
	}{
		{"initial question", DecodeInitialResult, `{"status":"NEEDS_INPUT","message":"Which behavior?"}`, Result{Status: StatusNeedsInput, Message: "Which behavior?"}, true},
		{"initial ready", DecodeInitialResult, `{"status":"READY_TO_WRITE","spec_id":"new-flow"}`, Result{Status: StatusReadyToWrite, SpecID: "new-flow"}, true},
		{"create", DecodeCreateResult, `{"status":"WRITTEN"}`, Result{Status: StatusWritten}, true},
		{"question", DecodeQuestionResult, `{"status":"ANSWERED","message":"The answer"}`, Result{Status: StatusAnswered, Message: "The answer"}, true},
		{"change question", DecodeChangeResult, `{"status":"NEEDS_INPUT","message":"Which variant?"}`, Result{Status: StatusNeedsInput, Message: "Which variant?"}, true},
		{"change ready", DecodeChangeResult, `{"status":"READY_TO_UPDATE"}`, Result{Status: StatusReadyToUpdate}, true},
		{"update", DecodeUpdateResult, `{"status":"UPDATED"}`, Result{Status: StatusUpdated}, true},
		{"unknown field", DecodeInitialResult, `{"status":"NEEDS_INPUT","message":"Question?","extra":true}`, Result{}, false},
		{"unknown status", DecodeInitialResult, `{"status":"OTHER"}`, Result{}, false},
		{"status from another turn", DecodeCreateResult, `{"status":"UPDATED"}`, Result{}, false},
		{"empty question", DecodeInitialResult, `{"status":"NEEDS_INPUT","message":"  "}`, Result{}, false},
		{"empty answer", DecodeQuestionResult, `{"status":"ANSWERED","message":""}`, Result{}, false},
		{"missing message", DecodeChangeResult, `{"status":"NEEDS_INPUT"}`, Result{}, false},
		{"forbidden spec id with question", DecodeInitialResult, `{"status":"NEEDS_INPUT","message":"Question?","spec_id":"valid"}`, Result{}, false},
		{"forbidden empty message", DecodeInitialResult, `{"status":"READY_TO_WRITE","spec_id":"valid","message":""}`, Result{}, false},
		{"forbidden null message", DecodeCreateResult, `{"status":"WRITTEN","message":null}`, Result{}, false},
		{"forbidden empty spec id", DecodeCreateResult, `{"status":"WRITTEN","spec_id":""}`, Result{}, false},
		{"forbidden message on change ready", DecodeChangeResult, `{"status":"READY_TO_UPDATE","message":"done"}`, Result{}, false},
		{"forbidden fields on update", DecodeUpdateResult, `{"status":"UPDATED","message":"done","spec_id":"valid"}`, Result{}, false},
		{"empty spec id", DecodeInitialResult, `{"status":"READY_TO_WRITE","spec_id":""}`, Result{}, false},
		{"invalid spec id", DecodeInitialResult, `{"status":"READY_TO_WRITE","spec_id":"CON"}`, Result{}, false},
		{"trailing JSON", DecodeUpdateResult, `{"status":"UPDATED"}{}`, Result{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.decode([]byte(test.input))
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatalf("decoded invalid result: %#v", got)
			}
			if test.valid && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSchemasExposeOnlyAllowedStatusAndFields(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want map[string][]string
	}{
		{"initial", InitialSchema(), map[string][]string{"NEEDS_INPUT": {"message", "status"}, "READY_TO_WRITE": {"spec_id", "status"}}},
		{"create", CreateSchema(), map[string][]string{"WRITTEN": {"status"}}},
		{"question", QuestionSchema(), map[string][]string{"ANSWERED": {"message", "status"}}},
		{"change", ChangeSchema(), map[string][]string{"NEEDS_INPUT": {"message", "status"}, "READY_TO_UPDATE": {"status"}}},
		{"update", UpdateSchema(), map[string][]string{"UPDATED": {"status"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := schemaVariants(t, test.data); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("schema variants = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPromptContracts(t *testing.T) {
	brief := "brief-data-7dd1"
	initial := InitialPrompt(brief)
	for _, fragment := range []string{
		"Изучи релевантные файлы текущего Git working tree до первого вопроса.",
		"Не создавай и не изменяй файлы на этапе уточнения.",
		"Задавай не более одного вопроса за turn.",
		"IDEA BRIEF:\n" + brief,
	} {
		if !strings.Contains(initial, fragment) {
			t.Errorf("initial prompt misses %q", fragment)
		}
	}

	directory := `C:\repo\docs\specs\new-flow`
	for name, prompt := range map[string]string{"create": CreatePrompt(directory), "update": UpdatePrompt(directory)} {
		if !strings.Contains(prompt, "Разрешённый каталог:\n"+directory+"\n") || !strings.Contains(prompt, "вне разрешённого каталога") {
			t.Errorf("%s prompt does not isolate the allowed path or write boundary", name)
		}
	}
	for name, prompt := range map[string]string{"question": QuestionPrompt("question-data-7dd1"), "change": ChangePrompt("change-data-7dd1")} {
		if !strings.Contains(prompt, "Сначала заново прочитай текущие файлы спецификации") || !strings.Contains(prompt, "изменяй файлы.") {
			t.Errorf("%s prompt misses its read-only contract", name)
		}
	}
	if !strings.HasSuffix(QuestionPrompt("question-data-7dd1"), "QUESTION:\nquestion-data-7dd1") {
		t.Error("question is not isolated after its label")
	}
	if !strings.HasSuffix(ChangePrompt("change-data-7dd1"), "CHANGE REQUEST:\nchange-data-7dd1") {
		t.Error("change request is not isolated after its label")
	}
}

func schemaVariants(t *testing.T, data json.RawMessage) map[string][]string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	variants := []any{root}
	if oneOf, ok := root["oneOf"].([]any); ok {
		variants = oneOf
	}
	got := make(map[string][]string, len(variants))
	for _, raw := range variants {
		variant := raw.(map[string]any)
		if variant["additionalProperties"] != false {
			t.Fatal("schema permits unknown fields")
		}
		properties := variant["properties"].(map[string]any)
		status := properties["status"].(map[string]any)["enum"].([]any)[0].(string)
		fields := make([]string, 0, len(properties))
		for field := range properties {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		required := variant["required"].([]any)
		if len(required) != len(fields) {
			t.Fatalf("status %s has optional or undeclared fields", status)
		}
		got[status] = fields
	}
	return got
}
