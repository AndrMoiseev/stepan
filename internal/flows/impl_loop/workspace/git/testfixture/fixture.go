// Package testfixture supports integration scenarios that require Git hooks and metadata.
package testfixture

import (
	"os/exec"
	"strings"
	"testing"
)

func Run(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
