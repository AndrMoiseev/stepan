package setting

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

func TestMergeRejectsProfilesAndTokenInsideFlow(t *testing.T) {
	for _, field := range []string{"profiles", "auth_token", "nessyapp"} {
		_, err := Merge(Sources{Project: json.RawMessage(`{"` + field + `":{}}`)})
		if err == nil || !strings.Contains(err.Error(), "flows.impl_loop") {
			t.Fatalf("misplaced %s: %v", field, err)
		}
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

func TestRoleProfileUsesAgreedDefaultsWithoutBuiltInProfiles(t *testing.T) {
	configuration, err := Merge(sources(t, ``, ``))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		RoleOrchestrator:  "medium",
		RoleBriefer:       "high",
		RoleImplementer:   "medium",
		RoleTaskReviewer:  "high",
		RoleExplorer:      "low",
		RoleFinalReviewer: "ultra",
		RoleBootstrapper:  "high",
	}
	for role, profile := range want {
		if got := configuration.RoleProfile(role); got != profile {
			t.Fatalf("profile for %q = %q, want %q", role, got, profile)
		}
	}
	if len(configuration.Profiles) != 0 {
		t.Fatalf("default profiles must not be built in: %#v", configuration.Profiles)
	}
}

func TestMergeProjectOverridesDefaultRole(t *testing.T) {
	configuration, err := Merge(sources(t, `{"profiles":{"review":{"provider":"codex"}}}`, `{"roles":{"final_reviewer":"review"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := configuration.RoleProfile(RoleFinalReviewer), "review"; got != want {
		t.Fatalf("final reviewer profile = %q, want %q", got, want)
	}
}

func TestValidateLoopRolesRequiresAllLoopProfilesButNotBootstrapper(t *testing.T) {
	_, err := Merge(sources(t, `{
  "profiles":{"low":{},"medium":{},"high":{},"ultra":{}},
  "roles":{"bootstrapper":"missing"}
}`, ``))
	if err == nil || !strings.Contains(err.Error(), `role "bootstrapper" references missing profile "missing"`) {
		t.Fatalf("explicit bootstrapper assignment must still resolve: %v", err)
	}

	configuration, err := Merge(sources(t, `{"profiles":{"low":{},"medium":{},"high":{},"ultra":{}}}`, ``))
	if err != nil {
		t.Fatal(err)
	}
	if err := configuration.ValidateLoopRoles(); err != nil {
		t.Fatalf("loop validation = %v", err)
	}
	profile, configured, err := configuration.BootstrapProfile()
	if err != nil || !configured || profile != "high" {
		t.Fatalf("bootstrap profile = %q, %t, %v", profile, configured, err)
	}

	configuration, err = Merge(sources(t, `{
  "profiles":{"low":{},"medium":{},"ultra":{}},
  "roles":{"briefer":"medium","task_reviewer":"medium"}
}`, ``))
	if err != nil {
		t.Fatal(err)
	}
	if err := configuration.ValidateLoopRoles(); err != nil {
		t.Fatalf("loop must not require bootstrapper: %v", err)
	}
	profile, configured, err = configuration.BootstrapProfile()
	if err != nil || configured || profile != "" {
		t.Fatalf("bootstrap fallback = %q, %t, %v", profile, configured, err)
	}
}

func TestValidateLoopRolesRejectsDeletedDefaultProfile(t *testing.T) {
	configuration, err := Merge(sources(t, `{"profiles":{"low":{},"medium":{},"high":{},"ultra":{}}}`, `{"profiles":{"high":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := configuration.ValidateLoopRoles(); err == nil || !strings.Contains(err.Error(), `role "briefer" references missing profile "high"`) {
		t.Fatalf("loop validation must reject deleted default: %v", err)
	}
}

func TestMergeRejectsMalformedMergeValues(t *testing.T) {
	for _, test := range []struct {
		name string
		user string
		want string
	}{
		{name: "section", user: `null`, want: "user implementation configuration: expected an implementation object"},
		{name: "profiles", user: `{"profiles":[]}`, want: "user implementation configuration: agentruntime.profiles must be an object"},
		{name: "profile", user: `{"profiles":{"medium":"not-an-object"}}`, want: "user implementation configuration: agentruntime.profiles.medium must be an object"},
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
		result.User, result.UserProfiles = splitTestSection(t, user)
	}
	if project != "" {
		result.Project, result.ProjectProfiles = splitTestSection(t, project)
	}
	return result
}

func splitTestSection(t *testing.T, raw string) (json.RawMessage, json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return json.RawMessage(raw), nil
	}
	profiles := fields["profiles"]
	delete(fields, "profiles")
	loop, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return loop, profiles
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
