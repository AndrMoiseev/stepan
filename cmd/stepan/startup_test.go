package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverImplementationStartupLeavesMissingStoreUntouched(t *testing.T) {
	home := t.TempDir()
	text, err := discoverImplementationStartup(context.Background(), t.TempDir(), func() (string, error) { return home, nil })
	if err != nil || text != "" {
		t.Fatalf("missing startup run = %q, %v", text, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".stepan")); !os.IsNotExist(err) {
		t.Fatalf("startup discovery created run store: %v", err)
	}
}
