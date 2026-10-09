package testfs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
)

// Directory supplies a known workspace root to controller tests. An empty
// value treats the start directory itself as the root.
type Directory string

var _ workspace.RootFinder = Directory("")

func (d Directory) FindRoot(ctx context.Context, start string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root := string(d)
	if root == "" {
		root = start
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	start, err = filepath.Abs(start)
	if err != nil {
		return "", err
	}
	start, err = filepath.EvalSymlinks(start)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, start)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside test workspace %q", start, root)
	}
	return filepath.Clean(root), nil
}
