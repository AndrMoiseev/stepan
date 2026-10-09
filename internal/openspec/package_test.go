package openspec

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReadsCompleteChangePackageAndVersionsEveryInput(t *testing.T) {
	repository := t.TempDir()
	writePackageFile(t, repository, "openspec/changes/add-widget/proposal.md", "proposal\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/design.md", "design\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/tasks.md", "- [ ] task\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/specs/widget/spec.md", "change spec\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/specs/widget/nested/compat.md", "compat spec\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/specs/widget/ignored.txt", "not Markdown\n")
	writePackageFile(t, repository, "openspec/specs/accounts/spec.md", "main accounts spec\n")
	writePackageFile(t, repository, "openspec/specs/billing/nested/spec.md", "main billing spec\n")

	pkg, err := Load(repository, "add-widget")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Change != "add-widget" || pkg.Version == "" {
		t.Fatalf("package identity = %#v", pkg)
	}
	if got, want := paths(pkg.ChangeSpecs), []string{
		"openspec/changes/add-widget/specs/widget/nested/compat.md",
		"openspec/changes/add-widget/specs/widget/spec.md",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change specs = %#v, want %#v", got, want)
	}
	if got, want := paths(pkg.MainSpecs), []string{
		"openspec/specs/accounts/spec.md",
		"openspec/specs/billing/nested/spec.md",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("main specs = %#v, want %#v", got, want)
	}
	for _, document := range pkg.Documents() {
		if document.Path == "" || document.Version == "" || document.Content == "" {
			t.Fatalf("incomplete versioned document: %#v", document)
		}
	}

	context := pkg.CompleteSpecification()
	for _, marker := range []string{
		"# Change proposal: openspec/changes/add-widget/proposal.md", "change spec", "- [ ] task",
		"# Main specification (select if relevant): openspec/specs/accounts/spec.md", "main billing spec",
	} {
		if !strings.Contains(context, marker) {
			t.Fatalf("complete specification omits %q\n%s", marker, context)
		}
	}
	if strings.Contains(context, "not Markdown") {
		t.Fatalf("complete specification unexpectedly includes non-Markdown input\n%s", context)
	}

	previousVersion := pkg.Version
	writePackageFile(t, repository, "openspec/specs/accounts/spec.md", "updated main accounts spec\n")
	updated, err := Load(repository, "add-widget")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == previousVersion || updated.MainSpecs[0].Version == pkg.MainSpecs[0].Version {
		t.Fatalf("main specification change did not update versions: before=%#v after=%#v", pkg, updated)
	}
}

func TestLoadRejectsUnavailableRequiredDocumentsAndUnsafeChangeNames(t *testing.T) {
	repository := t.TempDir()
	writePackageFile(t, repository, "openspec/changes/add-widget/proposal.md", "proposal\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/tasks.md", "tasks\n")
	if _, err := Load(repository, "add-widget"); !errors.Is(err, ErrDocumentUnavailable) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing design error = %v", err)
	}

	for _, change := range []string{"", ".", "..", "../other", `one\\two`} {
		if _, err := Load(repository, change); !errors.Is(err, ErrInvalidChange) {
			t.Fatalf("Load(%q) error = %v, want invalid change", change, err)
		}
	}
}

func TestLoadRequiresBothSpecificationDirectoriesButAllowsNoMainSpecificationDocuments(t *testing.T) {
	repository := t.TempDir()
	writePackageFile(t, repository, "openspec/changes/add-widget/proposal.md", "proposal\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/design.md", "design\n")
	writePackageFile(t, repository, "openspec/changes/add-widget/tasks.md", "tasks\n")
	writePackageFile(t, repository, "openspec/specs/.gitkeep", "")
	if _, err := Load(repository, "add-widget"); !errors.Is(err, ErrDocumentUnavailable) {
		t.Fatalf("missing change specs directory error = %v", err)
	}

	writePackageFile(t, repository, "openspec/changes/add-widget/specs/.gitkeep", "")
	pkg, err := Load(repository, "add-widget")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.ChangeSpecs) != 0 || len(pkg.MainSpecs) != 0 {
		t.Fatalf("empty specification directories = %#v", pkg)
	}
}

func paths(documents []Document) []string {
	paths := make([]string, len(documents))
	for index, document := range documents {
		paths[index] = document.Path
	}
	return paths
}

func writePackageFile(t *testing.T, repository, name, contents string) {
	t.Helper()
	path := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
