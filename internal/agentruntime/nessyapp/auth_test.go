package nessyapp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAuthEnvironmentOverride(t *testing.T) {
	base := []string{"UNCHANGED=value", "NESSY_CLI_DP_AUTH_TOKEN=B", "NESSY_CLI_DP_AUTH_TOKEN=C"}
	if runtime.GOOS == "windows" {
		base = append(base, "nessy_cli_dp_auth_token=D")
	}
	before := append([]string(nil), base...)
	env := isolatedEnv(base, "A")
	count := 0
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if environmentKey(key) == environmentKey("NESSY_CLI_DP_AUTH_TOKEN") {
			count++
			if value != "A" {
				t.Fatal("inherited token survived")
			}
		}
	}
	if count != 1 || !reflect.DeepEqual(base, before) {
		t.Fatal("duplicate token or mutated input")
	}
	if !strings.Contains(strings.Join(env, "\n"), "UNCHANGED=value") {
		t.Fatal("lost unrelated environment")
	}
}

func TestAuthInvalidBeforeProcessCreation(t *testing.T) {
	for _, token := range []string{"", " \t\n", "secret\x00suffix"} {
		config := Config{AuthToken: token, EnvelopeSchema: json.RawMessage(`{"type":"object"}`)}
		if value, err := StartRuntime(config); err == nil || value != nil {
			t.Fatal("invalid auth accepted by runtime")
		}
		process := NewProcess(config, "")
		if err := process.Start(); err == nil || process.command != nil {
			t.Fatal("invalid auth reached launcher")
		}
	}
	if _, present := reflect.TypeFor[Config]().FieldByName("Executable"); present {
		t.Fatal("executable override exposed")
	}
}

func TestConfiguredTokenReachesEveryProcessWithoutArgvOrParentMutation(t *testing.T) {
	testAuthToken(t)
	workspace := makeGitRoot(t)
	t.Setenv("GO_WANT_NESSYAPP_FAKE", "contract")
	t.Setenv("NESSY_CLI_DP_AUTH_TOKEN", "inherited-B")
	const token = "configured-A-not-in-parent"
	for i := 0; i < 2; i++ {
		metadata := filepath.Join(t.TempDir(), "metadata.json")
		t.Setenv("STEPAN_NESSYAPP_METADATA", metadata)
		process := NewProcess(Config{AuthToken: token, Workspace: workspace, JSONContract: JSONContract}, t.TempDir())
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = process.Close() })
		observed := readFakeMetadata(t, metadata)
		matches := 0
		for _, entry := range observed.Environment {
			key, value, _ := strings.Cut(entry, "=")
			if environmentKey(key) == environmentKey("NESSY_CLI_DP_AUTH_TOKEN") {
				matches++
				if value != token {
					t.Fatal("wrong child token")
				}
			}
		}
		if matches != 1 || strings.Contains(strings.Join(observed.Args, " "), token) {
			t.Fatal("token transport contract violated")
		}
		if os.Getenv("NESSY_CLI_DP_AUTH_TOKEN") != "inherited-B" {
			t.Fatal("parent mutated")
		}
		if _, err := process.Stdin().Write([]byte("done\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(process.Stdout()).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		if err := process.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredTokenDiagnosticAcrossWritesAndTruncation(t *testing.T) {
	const token = "configured-secret-not-from-environment"
	for _, prefix := range []string{"", strings.Repeat("x", maxDiagnosticBytes-len(token)/2)} {
		var diagnostic limitedDiagnostic
		diagnostic.setSecrets([]string{token})
		_, _ = diagnostic.Write([]byte(prefix + token[:9]))
		_, _ = diagnostic.Write([]byte(token[9:] + " " + token))
		if value := diagnostic.String(); strings.Contains(value, token) || strings.Contains(value, token[:9]) {
			t.Fatal("credential fragment leaked")
		}
	}
}

func TestCredentialDetectionBeforeACPDelivery(t *testing.T) {
	const token = "credential-marker"
	for _, payload := range []string{`{"result":"credential-marker"}`, `{"error":{"message":"credential-marker"}}`, `{"result":"credential-\u006darker"}`, `{"text":"{\"answer\":\"credential-\\u006darker\"}"}`} {
		decoder := newLineDecoder(strings.NewReader(payload + "\n"))
		decoder.authToken = token
		if _, err := decoder.decode(); err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("unsafe credential delivery")
		}
	}
	if containsCredential([]byte(`{"answer":"ordinary text"}`), token) {
		t.Fatal("ordinary result rejected")
	}
}
