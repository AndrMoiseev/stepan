package setting

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSelectChecksInDefaultsCWDToRepositoryRoot(t *testing.T) {
	repositoryRoot := t.TempDir()
	configuration := checkConfiguration(t, `{
	  "omitted":{"kind":"build","command":{"program":"go","args":["build"],"env":{"CGO_ENABLED":"0"}}},
	  "dot":{"kind":"tests","command":{"program":"go","args":[]},"cwd":"."}
	}`, `["omitted", "dot"]`)

	selection, err := configuration.SelectChecksIn(repositoryRoot, Platform{OS: "windows", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"omitted", "dot"} {
		check := selection.Checks[name]
		if check.CWD != repositoryRoot {
			t.Fatalf("%s cwd = %q, want repository root %q", name, check.CWD, repositoryRoot)
		}
	}
	check := selection.Checks["omitted"]
	if check.Command.Program != "go" || !reflect.DeepEqual(check.Command.Args, []string{"build"}) || !reflect.DeepEqual(check.Command.Env, map[string]string{"CGO_ENABLED": "0"}) {
		t.Fatalf("command changed while resolving cwd: %#v", check.Command)
	}
}

func TestSelectChecksInResolvesRelativeAndAbsoluteCWD(t *testing.T) {
	repositoryRoot := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "working")
	configuration := checkConfiguration(t, `{
  "relative":{"kind":"build","command":{"program":"tool","args":[]},"cwd":"tools/build"},
  "absolute":{"kind":"tests","command":{"program":"tool","args":[]},"cwd":`+quoteJSON(absolute)+`}
}`, `["relative", "absolute"]`)

	selection, err := configuration.SelectChecksIn(repositoryRoot, Platform{OS: "windows", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := selection.Checks["relative"].CWD, filepath.Join(repositoryRoot, "tools", "build"); got != want {
		t.Fatalf("relative cwd = %q, want %q", got, want)
	}
	if got, want := selection.Checks["absolute"].CWD, filepath.Clean(absolute); got != want {
		t.Fatalf("absolute cwd = %q, want %q", got, want)
	}
}

func TestResolveRulesFileResolvesRelativeAndAbsolutePaths(t *testing.T) {
	repositoryRoot := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "rules.md")
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "relative", value: "docs/rules/index.md", want: filepath.Join(repositoryRoot, "docs", "rules", "index.md")},
		{name: "absolute", value: absolute, want: filepath.Clean(absolute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := Configuration{RulesFile: []byte(quoteJSON(test.value))}
			got, err := configuration.ResolveRulesFile(repositoryRoot)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("rules_file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveRulesFileAllowsOmissionAndRejectsInvalidInputs(t *testing.T) {
	repositoryRoot := t.TempDir()
	if got, err := (Configuration{}).ResolveRulesFile(repositoryRoot); err != nil || got != "" {
		t.Fatalf("omitted rules_file = %q, %v", got, err)
	}
	if _, err := (Configuration{}).ResolveRulesFile("relative-root"); err == nil {
		t.Fatal("relative repository root succeeded")
	}
	for _, rulesFile := range [][]byte{[]byte(`null`), []byte(`[]`)} {
		configuration := Configuration{RulesFile: rulesFile}
		if _, err := configuration.ResolveRulesFile(repositoryRoot); err == nil {
			t.Fatalf("rules_file %s succeeded", rulesFile)
		}
	}
}

func TestPathResolutionTreatsHomeAndEnvironmentSyntaxLiterally(t *testing.T) {
	repositoryRoot := t.TempDir()
	literal := "~/$STEPAN_HOME/%STEPAN_HOME%/rules.md"
	configuration := checkConfiguration(t, `{
  "build":{"kind":"build","command":{"program":"tool","args":[]},"cwd":`+quoteJSON(literal)+`}
}`, `["build"]`)
	configuration.RulesFile = []byte(quoteJSON(literal))

	selection, err := configuration.SelectChecksIn(repositoryRoot, Platform{OS: "windows", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	rulesFile, err := configuration.ResolveRulesFile(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repositoryRoot, filepath.FromSlash(literal))
	if got := selection.Checks["build"].CWD; got != want {
		t.Fatalf("cwd = %q, want literal path %q", got, want)
	}
	if rulesFile != want {
		t.Fatalf("rules_file = %q, want literal path %q", rulesFile, want)
	}
}

func TestPathResolutionRejectsWindowsAmbiguousPathForms(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows path forms only")
	}
	repositoryRoot := t.TempDir()
	for _, value := range []string{`\rules\index.md`, `/rules/index.md`, filepath.VolumeName(repositoryRoot) + `rules\index.md`} {
		configuration := Configuration{RulesFile: []byte(quoteJSON(value))}
		if _, err := configuration.ResolveRulesFile(repositoryRoot); err == nil {
			t.Fatalf("ResolveRulesFile(%q) succeeded", value)
		}
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
