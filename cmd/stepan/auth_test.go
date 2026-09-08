package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/nessyapp"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNessyCredentialsDoNotReachFlowArtifacts(t *testing.T) {
	for _, mode := range []string{"error", "result"} {
		t.Run(mode, func(t *testing.T) {
			root := initializeCompositionRepository(t)
			observations := t.TempDir()
			t.Setenv("STEPAN_MAIN_NESSY_FAKE", "1")
			t.Setenv("STEPAN_MAIN_NESSY_OBSERVATIONS", observations)
			t.Setenv("STEPAN_MAIN_NESSY_GENERATION", "credential-test")
			t.Setenv("STEPAN_MAIN_NESSY_LEAK", mode)
			config := agentConfig{kind: agentNessy, executable: compositionExecutableName(t)}
			application, registry, session := newNessyCompositionApplication(t, root, config)
			t.Cleanup(func() { _ = registry.Close(); _ = session.Close() })
			progress, err := application.StartFeature("safe user request")
			if err == nil || strings.Contains(err.Error(), "test-auth-token") {
				t.Fatal("credential response was not safely rejected")
			}
			encoded, _ := json.Marshal(progress)
			if strings.Contains(string(encoded), "test-auth-token") {
				t.Fatal("credential in progress")
			}
			for _, directory := range []string{root, observations} {
				err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if entry.IsDir() {
						if entry.Name() == ".git" {
							return filepath.SkipDir
						}
						return nil
					}
					data, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					if strings.Contains(string(data), "test-auth-token") {
						t.Fatal("credential persisted")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestConfiguredRuntimeFactoryReadsOnceBeforeStarting(t *testing.T) {
	loads, starts := 0, 0
	token := "snapshot-A"
	load := func() (string, error) { loads++; return token, nil }
	starters := runtimeStarters{nessy: func(config nessyapp.Config) (agentruntime.Runtime, error) {
		starts++
		if config.AuthToken != "snapshot-A" {
			t.Fatal("snapshot changed")
		}
		return &compositionRuntime{}, nil
	}}
	factory, err := configuredRuntimeFactory(agentConfig{kind: agentNessy}, t.TempDir(), load, starters)
	if err != nil || loads != 1 || starts != 0 {
		t.Fatal("not loaded before runtime")
	}
	token = "snapshot-B"
	for i := 0; i < 2; i++ {
		if _, err := factory(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 1 || starts != 2 {
		t.Fatal("unexpected loading or starts")
	}
	factory, err = configuredRuntimeFactory(agentConfig{kind: agentNessy}, t.TempDir(), func() (string, error) { return "", errors.New("invalid settings") }, starters)
	if err == nil || factory != nil || starts != 2 {
		t.Fatal("started despite settings error")
	}
	for _, kind := range []agentKind{agentCodex, agentClaude} {
		_, err := configuredRuntimeFactory(agentConfig{kind: kind}, t.TempDir(), func() (string, error) { t.Fatal("other provider read Nessy settings"); return "", nil }, starters)
		if err != nil {
			t.Fatal(err)
		}
	}
}
