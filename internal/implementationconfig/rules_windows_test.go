//go:build windows

package implementationconfig

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction is the Windows directory-link mechanism that needs no Developer
// Mode or elevated symbolic-link privilege. The handle-based canonical path
// resolver exercises its link-boundary and cycle behavior.
func makeRulesDirectoryLink(link, target string) error {
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create test rules junction: %w: %s", err, output)
	}
	return nil
}

func TestValidateRulesFileRejectsDirectoryLinkEscape(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), "# index\n")
	escapeTarget := t.TempDir()
	writeRulesFixture(t, filepath.Join(escapeTarget, "outside.md"), "# outside\n")
	if err := makeRulesDirectoryLink(filepath.Join(repository, "rules", "escape"), escapeTarget); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("directory link escaping rules root succeeded")
	}
}

func TestValidateRulesFileAcceptsDirectoryLinkInsideRulesRoot(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[linked](linked/child.md)\n")
	writeRulesFixture(t, filepath.Join(rules, "target", "child.md"), "# child\n")
	if err := makeRulesDirectoryLink(filepath.Join(rules, "linked"), filepath.Join(rules, "target")); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("directory link inside rules root failed: %v", err)
	}
}

func TestValidateRulesFileChecksEachJunctionAliasRelativeToItsLexicalPath(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(rules, "source.go"), "package rules\n")
	writeRulesFixture(t, filepath.Join(rules, "a-target", "doc.md"), "[source](../source.go)\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[alias](z-links/alias/doc.md)\n")
	if err := os.MkdirAll(filepath.Join(rules, "z-links"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeRulesDirectoryLink(filepath.Join(rules, "z-links", "alias"), filepath.Join(rules, "a-target")); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("junction alias with a missing lexical-relative source target succeeded")
	}
}

func TestValidateRulesFileAcceptsJunctionToRepositorySourceDirectory(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(repository, "source", "main.go"), "package source\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[source](source-link/main.go)\n")
	if err := makeRulesDirectoryLink(filepath.Join(rules, "source-link"), filepath.Join(repository, "source")); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("junction to repository source directory failed: %v", err)
	}
}

func TestValidateRulesFileRejectsMarkdownReachedThroughJunctionOutsideRulesRoot(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(repository, "source", "README.md"), "# source\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "# index\n")
	if err := makeRulesDirectoryLink(filepath.Join(rules, "source-link"), filepath.Join(repository, "source")); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("Markdown document reached through a junction outside the rules root succeeded")
	}
}

func TestValidateRulesFileTerminatesDirectoryLinkCycle(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "# index\n")
	if err := makeRulesDirectoryLink(filepath.Join(rules, "cycle"), rules); err != nil {
		t.Fatal(err)
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("directory link cycle was not handled: %v", err)
	}
}
