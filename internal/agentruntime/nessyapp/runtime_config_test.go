//go:build process_integration

package nessyapp

import "testing"

func TestNessyArgsCarryConfiguredModel(t *testing.T) {
	args := nessyArgs("contract", "artifact", "qwen-coder")
	if len(args) < 2 {
		t.Fatalf("Nessy args = %#v", args)
	}
	if got, want := args[len(args)-2:], []string{"--model", "qwen-coder"}; got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Nessy args = %#v, want model suffix %#v", args, want)
	}
}
