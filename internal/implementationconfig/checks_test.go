package implementationconfig

import (
	"reflect"
	"testing"
)

func TestSelectChecksUsesCompleteHostPlatformCommand(t *testing.T) {
	configuration := checkConfiguration(t, `{
  "build": {
    "kind": "build",
    "command": {"program":"go","args":["build"],"env":{"CGO_ENABLED":"0"}},
    "platforms": {"windows/amd64":{"program":"go","args":["build","-o","stepan.exe"],"env":{"GOOS":"darwin","GOARCH":"arm64"}}}
  },
  "tests": {"kind":"tests","command":{"program":"go","args":["test","./..."]}}
}`, `["build", "tests"]`)

	selection, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	build := selection.Checks["build"]
	if !build.Available || build.Command.Program != "go" {
		t.Fatalf("build selection = %#v", build)
	}
	if want := []string{"build", "-o", "stepan.exe"}; !reflect.DeepEqual(build.Command.Args, want) {
		t.Fatalf("args = %#v, want %#v", build.Command.Args, want)
	}
	if want := map[string]string{"GOOS": "darwin", "GOARCH": "arm64"}; !reflect.DeepEqual(build.Command.Env, want) {
		t.Fatalf("env = %#v, want %#v", build.Command.Env, want)
	}
	if build.TimeoutSeconds != defaultCheckTimeoutSeconds {
		t.Fatalf("timeout = %d, want %d", build.TimeoutSeconds, defaultCheckTimeoutSeconds)
	}
}

func TestSelectChecksFallsBackToCommonCommandOnOtherPlatform(t *testing.T) {
	configuration := checkConfiguration(t, `{
  "build": {"kind":"build","command":{"program":"go","args":["build"],"env":{"CGO_ENABLED":"0"}},"platforms":{"windows/amd64":{"program":"go","args":["build","-o","stepan.exe"]}}}
}`, `["build"]`)

	selection, err := configuration.SelectChecks(Platform{OS: "darwin", Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	check := selection.Checks["build"]
	if !check.Available || !reflect.DeepEqual(check.Command.Args, []string{"build"}) || !reflect.DeepEqual(check.Command.Env, map[string]string{"CGO_ENABLED": "0"}) {
		t.Fatalf("common command was not selected: %#v", check)
	}
}

func TestSelectChecksRejectsUnknownRequiredCheck(t *testing.T) {
	configuration := checkConfiguration(t, `{"build":{"kind":"build","command":{"program":"go"}}}`, `["missing"]`)
	if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != `project implementation configuration: required_checks references unknown check "missing"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectChecksRejectsRequiredCheckWithoutApplicableCommand(t *testing.T) {
	configuration := checkConfiguration(t, `{
  "build": {"kind":"build","platforms":{"darwin/arm64":{"program":"go","args":["build"]}}}
}`, `["build"]`)
	if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != `project implementation configuration: required check "build" has no command for platform "windows/amd64"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectChecksMarksUnsupportedAdditionalCheckUnavailable(t *testing.T) {
	configuration := checkConfiguration(t, `{
  "build": {"kind":"build","command":{"program":"go","args":["build"]}},
  "mac_only": {"kind":"tests","platforms":{"darwin/arm64":{"program":"go","args":["test","./..."]}}}
}`, `["build"]`)

	selection, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if check := selection.Checks["mac_only"]; check.Available || check.Command.Program != "" {
		t.Fatalf("unsupported additional check must remain unavailable: %#v", check)
	}
}

func TestSelectChecksDoesNotBorrowCommonFieldsForIncompletePlatformCommand(t *testing.T) {
	configuration := checkConfiguration(t, `{
  "build": {"kind":"build","command":{"program":"go","args":["build"]},"platforms":{"windows/amd64":{"args":["build","-o","stepan.exe"]}}}
}`, `["build"]`)
	if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != `project implementation configuration: required check "build" has no command for platform "windows/amd64"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectChecksValidatesKindsAndRequiredChecks(t *testing.T) {
	for _, test := range []struct {
		name, checks, required, want string
	}{
		{"kind", `{"build":{"kind":"generate","command":{"program":"go"}}}`, `["build"]`, `project implementation configuration: checks.build has unsupported kind "generate"`},
		{"required", `{"build":{"kind":"build","command":{"program":"go"}}}`, `[]`, "project implementation configuration: required_checks must be a non-empty array of check names"},
		{"required null", `{"build":{"kind":"build","command":{"program":"go"}}}`, `null`, "project implementation configuration: required_checks must be a non-empty array of check names"},
		{"required empty", `{"build":{"kind":"build","command":{"program":"go"}}}`, `[""]`, "project implementation configuration: required_checks must contain non-empty check names"},
		{"required element null", `{"build":{"kind":"build","command":{"program":"go"}}}`, `[null]`, "project implementation configuration: required_checks must contain non-empty check names"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := checkConfiguration(t, test.checks, test.required)
			if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != test.want {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSelectChecksRejectsEmptyCheckDefinitionName(t *testing.T) {
	configuration := checkConfiguration(t, `{"":{"kind":"tests","command":{"program":"go"}}}`, `[""]`)
	if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != "project implementation configuration: checks must use non-empty names" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectChecksRequiresRequiredChecksField(t *testing.T) {
	configuration, err := Merge(sources(t, "", `{"checks":{"build":{"kind":"build","command":{"program":"go"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.SelectChecks(Platform{OS: "windows", Architecture: "amd64"}); err == nil || err.Error() != "project implementation configuration: required_checks must be a non-empty array of check names" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func checkConfiguration(t *testing.T, checks, required string) Configuration {
	t.Helper()
	configuration, err := Merge(sources(t, "", `{"checks":`+checks+`,"required_checks":`+required+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}
