package implementationconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRulesFileAcceptsNestedReferenceLinksImagesAndWebLinks(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "code.go"), "package example\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "assets", "logo.png"), "png")
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), strings.Join([]string{
		"[child][child]",
		"![logo][logo]",
		"[offline web](https://127.0.0.1:1/no-network)",
		"[child]: nested/child.md",
		"[logo]: assets/logo.png",
	}, "\n"))
	child := filepath.Join(repository, "rules", "nested", "child.md")
	writeRulesFixture(t, child, "[source](../../code.go)\n")

	validation, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository)
	if err != nil {
		t.Fatal(err)
	}
	if validation.File != filepath.Join(repository, "rules", "index.md") {
		t.Fatalf("rules file = %q", validation.File)
	}
	if validation.Root != filepath.Join(repository, "rules") {
		t.Fatalf("rules root = %q", validation.Root)
	}

	// A later call re-reads the document rather than relying on a snapshot.
	writeRulesFixture(t, child, "[missing](missing.go)\n")
	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("changed rules document with a missing target succeeded")
	}
}

func TestValidateRulesFileScansUnlinkedMarkdownDocuments(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), "# index\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "unlinked.md"), "[missing](missing.go)\n")

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("unlinked invalid Markdown document was not scanned")
	}
}

func TestValidateRulesFileRejectsMissingAndEscapingTargets(t *testing.T) {
	for _, test := range []struct {
		name  string
		link  string
		setup func(t *testing.T, repository string)
	}{
		{
			name: "missing target",
			link: "missing.go",
		},
		{
			name: "Markdown outside rules root",
			link: "../outside.md",
			setup: func(t *testing.T, repository string) {
				writeRulesFixture(t, filepath.Join(repository, "outside.md"), "# outside\n")
			},
		},
		{
			name: "source outside repository with dotdot",
			link: "../../outside.go",
			setup: func(t *testing.T, repository string) {
				writeRulesFixture(t, filepath.Join(filepath.Dir(repository), "outside.go"), "package outside\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			if test.setup != nil {
				test.setup(t, repository)
			}
			writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), "[target]("+test.link+")\n")
			if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
				t.Fatal("escaping or missing target succeeded")
			}
		})
	}
}

func TestValidateRulesFileClassifiesLexicalMarkdownTargetsAndAbsoluteFragments(t *testing.T) {
	repository := t.TempDir()
	outsideMarkdown := filepath.Join(repository, "outside.md")
	writeRulesFixture(t, outsideMarkdown, "# outside\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), "[outside]("+filepath.ToSlash(outsideMarkdown)+"#section)\n")

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("absolute Markdown target outside rules root with a fragment succeeded")
	}
}

func TestValidateRulesFileAcceptsEscapedLocalDestination(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "rules", "named & file.go"), "package named\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "bracket[name].go"), "package named\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "ampersand&name.go"), "package named\n")
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), strings.Join([]string{
		"[percent](<named%20%26%20file.go>)",
		"[backslash](bracket\\[name\\].go)",
		"[entity](ampersand&amp;name.go)",
	}, "\n"))

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("escaped destination failed: %v", err)
	}
}

func TestValidateRulesFileRejectsInvalidEntryFile(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.txt"), "rules")
	if _, err := rulesConfiguration("rules/index.txt").ValidateRulesFile(repository); err == nil {
		t.Fatal("non-Markdown rules_file succeeded")
	}
	if _, err := rulesConfiguration("missing.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("missing rules_file succeeded")
	}
	outsideRulesFile := filepath.Join(t.TempDir(), "outside.md")
	writeRulesFixture(t, outsideRulesFile, "# outside\n")
	if _, err := rulesConfiguration(outsideRulesFile).ValidateRulesFile(repository); err == nil {
		t.Fatal("rules_file outside repository succeeded")
	}
}

func TestValidateRulesFileAllowsOmittedRulesFile(t *testing.T) {
	validation, err := (Configuration{}).ValidateRulesFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if validation != (RulesFileValidation{}) {
		t.Fatalf("omitted rules file = %#v", validation)
	}
}

func rulesConfiguration(rulesFile string) Configuration {
	return Configuration{RulesFile: []byte(quoteJSON(rulesFile))}
}

func writeRulesFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
