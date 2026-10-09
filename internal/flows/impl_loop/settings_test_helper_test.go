package impl_loop

import (
	"encoding/json"

	"github.com/AndrMoiseev/stepan/internal/setting"
)

// loopTestSources adapts pre-schema test fixtures to the split settings
// members. Production never accepts profiles inside flows.impl_loop.
func loopTestSources(user, project json.RawMessage) setting.Sources {
	u, up := splitLoopTestSection(user)
	p, pp := splitLoopTestSection(project)
	return setting.Sources{User: u, Project: p, UserProfiles: up, ProjectProfiles: pp}
}

func splitLoopTestSection(raw json.RawMessage) (json.RawMessage, json.RawMessage) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw, nil
	}
	profiles := fields["profiles"]
	delete(fields, "profiles")
	loop, _ := json.Marshal(fields)
	return loop, profiles
}
