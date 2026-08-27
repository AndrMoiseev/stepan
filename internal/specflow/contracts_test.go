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
