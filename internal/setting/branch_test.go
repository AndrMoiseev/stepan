package setting

import (
	"encoding/json"
	"testing"
)

func TestMainBranchName(t *testing.T) {
	for _, test := range []struct {
		raw  json.RawMessage
		want string
		bad  bool
	}{
		{nil, "", false},
		{json.RawMessage(`"main"`), "main", false},
		{json.RawMessage(`null`), "", true},
		{json.RawMessage(`" "`), "", true},
	} {
		got, err := (Configuration{MainBranch: test.raw}).MainBranchName()
		if got != test.want || (err != nil) != test.bad {
			t.Fatalf("raw %s: got %q, %v", test.raw, got, err)
		}
	}
}
