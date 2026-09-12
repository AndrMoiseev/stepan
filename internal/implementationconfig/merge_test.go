package implementationconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeInheritsProfilesRolesAndIndividualLimits(t *testing.T) {
	configuration, err := Merge(sources(t, `{
  "profiles":{"low":{"provider":"a"},"medium":{"provider":"b"}},
  "roles":{"executor":"medium","explorer":"low"},
  "limits":{"review_rounds":3,"explorer_characters":12000}
}`, `{
  "roles":{"executor":"low"},
  "limits":{"review_rounds":5}
}`))
	if err != nil {
		t.Fatal(err)
	}
	assertRawJSON(t, configuration.Profiles["medium"], `{"provider":"b"}`)
	if got, want := configuration.Roles["executor"], "low"; got != want {
		t.Fatalf("executor role = %q, want %q", got, want)
	}
	if got, want := configuration.Roles["explorer"], "low"; got != want {
		t.Fatalf("explorer role = %q, want %q", got, want)
	}
	assertRawJSON(t, configuration.Limits["review_rounds"], `5`)
	assertRawJSON(t, configuration.Limits["explorer_characters"], `12000`)
}

func TestMergeProjectProfileFullyReplacesUserProfile(t *testing.T) {
	configuration, err := Merge(sources(t, `{"profiles":{"high":{"provider":"user","model":"old","reasoning":"high"}},"roles":{"reviewer":"high"}}`, `{"profiles":{"high":{"provider":"project"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertRawJSON(t, configuration.Profiles["high"], `{"provider":"project"}`)
}

func TestMergeProjectProfileNullDeletesInheritedProfileAndRejectsReference(t *testing.T) {
	_, err := Merge(sources(t, `{"profiles":{"high":{"provider":"user"}},"roles":{"reviewer":"high"}}`, `{"profiles":{"high":null}}`))
	if err == nil || !strings.Contains(err.Error(), `role "reviewer" references missing profile "high"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeProjectProfileNullDeletesUnreferencedInheritedProfile(t *testing.T) {
	configuration, err := Merge(sources(t, `{"profiles":{"low":{"provider":"user"}}}`, `{"profiles":{"low":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := configuration.Profiles["low"]; ok {
		t.Fatalf("deleted profile remained in effective configuration: %#v", configuration.Profiles)
	}
}

func TestMergeRejectsEveryProjectOnlyUserFieldEvenNull(t *testing.T) {
	for _, field := range []string{"checks", "required_checks", "rules_file", "main_branch"} {
		t.Run(field, func(t *testing.T) {
			_, err := Merge(sources(t, `{"`+field+`":null}`, ``))
			if err == nil || err.Error() != "user implementation configuration: "+field+" is project-only" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMergeKeepsProjectCheckFieldsRawForLaterValidation(t *testing.T) {
	configuration, err := Merge(sources(t, ``, `{
  "checks":{"build":{"not_checked_until_task_2_3":true}},
  "required_checks":[],
  "rules_file":null,
  "main_branch":null
}`))
	if err != nil {
		t.Fatal(err)
	}
	assertRawJSON(t, configuration.Checks, `{"build":{"not_checked_until_task_2_3":true}}`)
	assertRawJSON(t, configuration.RequiredChecks, `[]`)
	assertRawJSON(t, configuration.RulesFile, `null`)
	assertRawJSON(t, configuration.MainBranch, `null`)
}

func TestMergeRejectsRemainingReferenceToMissingProfile(t *testing.T) {
	_, err := Merge(sources(t, `{"roles":{"executor":"medium"}}`, ``))
	if err == nil || !strings.Contains(err.Error(), `role "executor" references missing profile "medium"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeRejectsMalformedMergeValues(t *testing.T) {
	for _, test := range []struct {
		name string
		user string
		want string
	}{
		{name: "section", user: `null`, want: "user implementation configuration: expected an implementation object"},
		{name: "profiles", user: `{"profiles":[]}`, want: "user implementation configuration: profiles must be an object"},
		{name: "profile", user: `{"profiles":{"medium":"not-an-object"}}`, want: "user implementation configuration: profiles.medium must be an object"},
		{name: "roles", user: `{"roles":{"executor":null}}`, want: "user implementation configuration: roles must be an object of profile names"},
		{name: "limits", user: `{"limits":null}`, want: "user implementation configuration: limits must be an object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Merge(sources(t, test.user, ``))
			if err == nil || err.Error() != test.want {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func sources(t *testing.T, user, project string) Sources {
	t.Helper()
	result := Sources{}
	if user != "" {
		result.User = json.RawMessage(user)
	}
	if project != "" {
		result.Project = json.RawMessage(project)
	}
	return result
}

func assertRawJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON mismatch: got %s, want %s", gotJSON, wantJSON)
	}
}
