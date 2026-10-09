//go:build git_integration

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestorePathsPreservesNormalizedProtectedBytes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, attribute string
		configure       func(*testing.T, string)
	}{
		{"autocrlf", "protected.txt text\n", func(t *testing.T, r string) { gitFixture(t, r, "config", "core.autocrlf", "true") }},
		{"clean filter", "protected.txt filter=stepan\n", func(t *testing.T, r string) {
			gitFixture(t, r, "config", "filter.stepan.clean", "tr -d '\\r'")
			gitFixture(t, r, "config", "filter.stepan.smudge", "cat")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newGitWorkspace(t)
			test.configure(t, repository)
			writeGitWorkspaceFile(t, filepath.Join(repository, ".gitattributes"), test.attribute)
			writeGitWorkspaceFile(t, filepath.Join(repository, "protected.txt"), "before\r\n")
			gitFixture(t, repository, "add", ".gitattributes", "protected.txt")
			gitFixture(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "filtered")

			control := Control{}
			before, err := control.Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			writeGitWorkspaceFile(t, filepath.Join(repository, "protected.txt"), "agent\n")
			after, err := control.Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := control.RestorePaths(context.Background(), repository, before, after, []string{"protected.txt"}); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(filepath.Join(repository, "protected.txt"))
			if err != nil || string(contents) != "before\r\n" {
				t.Fatalf("restored bytes = %q, %v", contents, err)
			}
		})
	}
}
