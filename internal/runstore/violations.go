package runstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const violationJournalFileName = "violations.jsonl"

// ViolationRecord is one observed unauthorized file mutation by an agent
// call. It is intentionally kept outside the primary state journal: it is
// diagnostic evidence and never a source of implementation-flow transitions.
type ViolationRecord struct {
	Role               string   `json:"role"`
	CallID             string   `json:"call_id"`
	Paths              []string `json:"paths"`
	ViolatedConstraint string   `json:"violated_constraint"`
	RestorationResult  string   `json:"restoration_result"`
}

// ViolationJournalPath reports the run-local, append-only violation journal.
func (r *Run) ViolationJournalPath() string {
	if r == nil {
		return ""
	}
	return filepath.Join(r.directory, violationJournalFileName)
}

// AppendViolation synchronously appends one independently readable JSONL
// record. A failed append is surfaced to the controller; it must not pretend
// that a prohibited edit was safely accounted for.
func (r *Run) AppendViolation(record ViolationRecord) error {
	if r == nil {
		return fmt.Errorf("append violation: nil run")
	}
	if err := requireDirectory(r.directory); err != nil {
		return err
	}
	if strings.TrimSpace(record.Role) == "" || strings.TrimSpace(record.CallID) == "" || strings.TrimSpace(record.ViolatedConstraint) == "" || strings.TrimSpace(record.RestorationResult) == "" || len(record.Paths) == 0 {
		return fmt.Errorf("append violation: incomplete record")
	}
	record.Paths = append([]string(nil), record.Paths...)
	sort.Strings(record.Paths)
	for _, path := range record.Paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("append violation: empty path")
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode violation: %w", err)
	}
	path := r.ViolationJournalPath()
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: violation journal is unsafe", ErrUnsafePath)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect violation journal: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open violation journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("append violation journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync violation journal: %w", err)
	}
	return nil
}
