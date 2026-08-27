package specflow

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestInitialContractRequiresWrittenFeatureID(t *testing.T) {
	result, err := DecodeInitialResult([]byte(`{"status":"WRITTEN","feature_id":"new-feature"}`))
	if err != nil || !reflect.DeepEqual(result, Result{Status: StatusWritten, SpecID: "new-feature"}) {
		t.Fatalf("result = %#v, %v", result, err)
	}
	for _, value := range []string{
		`{"status":"WRITTEN"}`,
		`{"status":"WRITTEN","feature_id":"CON"}`,
		`{"status":"READY_TO_WRITE","feature_id":"old-flow"}`,
		`{"status":"WRITTEN","feature_id":"new-flow","message":"extra"}`,
	} {
		if _, err := DecodeInitialResult([]byte(value)); err == nil {
			t.Fatalf("accepted invalid initial result: %s", value)
		}
	}
	if bytes.Contains(InitialSchema(), []byte(`"oneOf"`)) {
		t.Fatalf("initial schema uses oneOf: %s", InitialSchema())
	}
}

func TestInitialPromptCreatesDraftInFeaturesDirectory(t *testing.T) {
	directory := `C:\repo\docs\changes\features`
	prompt := InitialPrompt("brief-data", directory)
	for _, fragment := range []string{
		"Не спрашивай уточнений",
		"Сразу выбери",
		"feature_id",
		"Разрешённый каталог для всех новых черновиков:\n" + directory,
		"верни WRITTEN и выбранный feature_id",
		"FEATURE BRIEF:\nbrief-data",
	} {
		if !strings.Contains(prompt, fragment) {
			t.Errorf("prompt misses %q", fragment)
		}
	}
}

func TestPromptTemplatesSeparateSystemRulesFromUserArtifact(t *testing.T) {
	system, err := promptFiles.ReadFile("prompts/initial-system.md")
	if err != nil {
		t.Fatal(err)
	}
	user, err := promptFiles.ReadFile("prompts/initial-user.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(system), "{{brief}}") || !strings.Contains(string(user), "{{brief}}") {
		t.Fatalf("initial brief is not isolated in the user template: system=%q user=%q", system, user)
	}
	prompt := InitialPrompt("{{unexpanded}}", "features")
	if !strings.Contains(prompt, "{{unexpanded}}") || strings.Contains(prompt, "{{brief}}") {
		t.Fatalf("user artifact was not rendered literally: %q", prompt)
	}
}
