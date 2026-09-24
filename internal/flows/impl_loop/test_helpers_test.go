package impl_loop

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func mustTransientControllerStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.NewTransient(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func readViolationRecords(t *testing.T, run *runstore.Run) []runstore.ViolationRecord {
	t.Helper()
	contents, err := os.ReadFile(run.ViolationJournalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	records := make([]runstore.ViolationRecord, 0, len(lines))
	for _, line := range lines {
		var record runstore.ViolationRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}
