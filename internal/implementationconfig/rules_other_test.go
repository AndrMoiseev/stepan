//go:build !windows

package implementationconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func makeNativeRulesLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRulesFileRejectsNativeDirectoryLinkEscape(t *testing.T) {
	repository := t.TempDir()
	writeRulesFixture(t, filepath.Join(repository, "rules", "index.md"), "# index\n")
	escapeTarget := t.TempDir()
	writeRulesFixture(t, filepath.Join(escapeTarget, "outside.md"), "# outside\n")
	makeNativeRulesLink(t, filepath.Join(repository, "rules", "escape"), escapeTarget)

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("native directory link escaping rules root succeeded")
	}
}

func TestValidateRulesFileAcceptsNativeDirectoryLinkInsideRulesRoot(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[linked](linked/child.md)\n")
	writeRulesFixture(t, filepath.Join(rules, "target", "child.md"), "# child\n")
	makeNativeRulesLink(t, filepath.Join(rules, "linked"), filepath.Join(rules, "target"))

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("native directory link inside rules root failed: %v", err)
	}
}

func TestValidateRulesFileChecksEachNativeLinkAliasRelativeToItsLexicalPath(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(rules, "source.go"), "package rules\n")
	writeRulesFixture(t, filepath.Join(rules, "a-target", "doc.md"), "[source](../source.go)\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[alias](z-links/alias/doc.md)\n")
	if err := os.MkdirAll(filepath.Join(rules, "z-links"), 0o700); err != nil {
		t.Fatal(err)
	}
	makeNativeRulesLink(t, filepath.Join(rules, "z-links", "alias"), filepath.Join(rules, "a-target"))

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("native link alias with a missing lexical-relative source target succeeded")
	}
}

func TestValidateRulesFileAcceptsNativeLinkToRepositorySourceDirectory(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(repository, "source", "main.go"), "package source\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[source](source-link/main.go)\n")
	makeNativeRulesLink(t, filepath.Join(rules, "source-link"), filepath.Join(repository, "source"))

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err != nil {
		t.Fatalf("native link to repository source directory failed: %v", err)
	}
}

func TestValidateRulesFileRejectsMarkdownReachedThroughNativeLinkOutsideRulesRoot(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	writeRulesFixture(t, filepath.Join(repository, "source", "README.md"), "# source\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "# index\n")
	makeNativeRulesLink(t, filepath.Join(rules, "source-link"), filepath.Join(repository, "source"))

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("Markdown document reached through a native link outside the rules root succeeded")
	}
}

func TestValidateRulesFileRejectsNativeMarkdownFileLinkEscape(t *testing.T) {
	repository := t.TempDir()
	rules := filepath.Join(repository, "rules")
	outside := filepath.Join(repository, "outside.txt")
	writeRulesFixture(t, outside, "outside\n")
	writeRulesFixture(t, filepath.Join(rules, "index.md"), "[outside](alias.md)\n")
	alias := filepath.Join(rules, "alias.md")
	makeNativeRulesLink(t, alias, outside)

	repositoryRoot, err := canonicalRulesDirectory(repository, "repository root")
	if err != nil {
		t.Fatal(err)
	}
	rulesRoot, err := canonicalRulesDirectory(rules, "rules root")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&rulesValidator{repository: repositoryRoot, rulesRoot: rulesRoot}).validateDestination(filepath.Join(rules, "index.md"), "alias.md"); err == nil {
		t.Fatal("lexical Markdown alias to non-Markdown target outside rules root succeeded")
	}

	if _, err := rulesConfiguration("rules/index.md").ValidateRulesFile(repository); err == nil {
		t.Fatal("native Markdown file link escaping rules root succeeded")
	}
}
