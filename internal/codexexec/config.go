package codexexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type Sandbox string

const (
	ReadOnly       Sandbox = "read-only"
	WorkspaceWrite Sandbox = "workspace-write"
)

type ConfigMode string

const (
	Isolated  ConfigMode = "isolated"
	Inherited ConfigMode = "inherited"
)

type Config struct {
	Executable   string
	CodexVersion string
	Workspace    string
	Prompt       []byte
	Sandbox      Sandbox
	SchemaPath   string
	Timeout      time.Duration
	ArtifactDir  string
	SessionID    string
	Nonce        string
	ConfigMode   ConfigMode
	IOGrace      time.Duration
}

func (c Config) Validate() (Config, error) {
	var err error
	c.Executable, err = resolveExecutable(c.Executable)
	if err != nil {
		return Config{}, err
	}
	if err := requireDir("workspace", c.Workspace); err != nil {
		return Config{}, err
	}
	if err := requireFile("schema", c.SchemaPath); err != nil {
		return Config{}, err
	}
	if !filepath.IsAbs(c.ArtifactDir) {
		return Config{}, errors.New("artifact directory must be absolute")
	}
	if len(c.Prompt) == 0 || !utf8.Valid(c.Prompt) {
		return Config{}, errors.New("prompt must be non-empty UTF-8")
	}
	if c.Sandbox != ReadOnly && c.Sandbox != WorkspaceWrite {
		return Config{}, fmt.Errorf("unsupported sandbox %q", c.Sandbox)
	}
	if c.ConfigMode != Isolated && c.ConfigMode != Inherited {
		return Config{}, fmt.Errorf("unsupported config mode %q", c.ConfigMode)
	}
	if c.Timeout <= 0 {
		return Config{}, errors.New("timeout must be positive")
	}
	if c.SessionID != "" && strings.TrimSpace(c.SessionID) == "" {
		return Config{}, errors.New("session ID must be non-empty when supplied")
	}
	if strings.TrimSpace(c.Nonce) == "" {
		return Config{}, errors.New("expected nonce is required")
	}
	return c, nil
}

func (c Config) Args() []string {
	lastMessage := filepath.Join(c.ArtifactDir, "last-message.json")
	var args []string
	if c.SessionID == "" {
		args = []string{
			"exec", "--json", "--color", "never",
			"--sandbox", string(c.Sandbox),
			"-c", `approval_policy="never"`,
			"--output-schema", c.SchemaPath,
			"--output-last-message", lastMessage,
			"--cd", c.Workspace,
		}
	} else {
		args = []string{
			"exec", "resume", "--json",
			"--output-schema", c.SchemaPath,
			"--output-last-message", lastMessage,
			"-c", `approval_policy="never"`,
		}
	}
	if c.ConfigMode == Isolated {
		args = append(args, "--ignore-user-config", "--ignore-rules")
	}
	if c.SessionID != "" {
		args = append(args, c.SessionID)
	}
	return append(args, "-")
}

func resolveExecutable(name string) (string, error) {
	if name == "" {
		return "", errors.New("executable is required")
	}
	if filepath.IsAbs(name) {
		if err := requireFile("executable", name); err != nil {
			return "", err
		}
		return filepath.Clean(name), nil
	}
	if filepath.Base(name) != name {
		return "", errors.New("executable must be an absolute path or a name resolved through PATH")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make executable path absolute: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	if filepath.Clean(filepath.Dir(path)) == filepath.Clean(cwd) {
		return "", errors.New("refusing executable resolved from the current directory")
	}
	return path, nil
}

func requireDir(label, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be absolute", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", label, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", label)
	}
	return nil
}

func requireFile(label, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be absolute", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	return nil
}
