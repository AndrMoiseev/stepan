package specflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type MemLogEventKind string

const (
	MemLogBrief            MemLogEventKind = "brief"
	MemLogUserMessage      MemLogEventKind = "user_message"
	MemLogAgentMessage     MemLogEventKind = "agent_message"
	MemLogDecision         MemLogEventKind = "decision"
	MemLogRevisionDecision MemLogEventKind = "revision_decision"
	MemLogDiff             MemLogEventKind = "diff"
	MemLogReview           MemLogEventKind = "review"
	MemLogAttempt          MemLogEventKind = "attempt"
	MemLogError            MemLogEventKind = "error"
	MemLogApproval         MemLogEventKind = "approval"
	MemLogCommit           MemLogEventKind = "commit"
	MemLogRecovery         MemLogEventKind = "recovery"
	MemLogSession          MemLogEventKind = "session"
)

func (k MemLogEventKind) Valid() bool {
	switch k {
	case MemLogBrief, MemLogUserMessage, MemLogAgentMessage, MemLogDecision,
		MemLogRevisionDecision, MemLogDiff, MemLogReview, MemLogAttempt,
		MemLogError, MemLogApproval, MemLogCommit, MemLogRecovery, MemLogSession:
		return true
	default:
		return false
	}
}

// MemLogEntry is both the append interface and the recovery representation of
// one human-readable memory-log record. Sequence and checksum are assigned by
// the journal implementation, never by callers.
type MemLogEntry struct {
	Sequence  uint64
	Stage     Stage
	Role      Role
	Kind      MemLogEventKind
	At        time.Time
	StateHash string
	Body      string
	Checksum  string
}

func NewMemLogEntry(stage Stage, role Role, kind MemLogEventKind, at time.Time, body string) (MemLogEntry, error) {
	entry := MemLogEntry{Stage: stage, Role: role, Kind: kind, At: at, Body: strings.TrimSpace(body)}
	if err := validateMemLogEntry(entry, false); err != nil {
		return MemLogEntry{}, err
	}
	return entry, nil
}

func validateMemLogEntry(entry MemLogEntry, stored bool) error {
	if !entry.Stage.Valid() || !entry.Role.Valid() || !entry.Kind.Valid() {
		return fmt.Errorf("%w: invalid mem-log stage, role, or event kind", ErrInvalidDomainValue)
	}
	if entry.At.IsZero() {
		return fmt.Errorf("%w: mem-log timestamp is required", ErrInvalidDomainValue)
	}
	if strings.TrimSpace(entry.Body) == "" {
		return fmt.Errorf("%w: mem-log body is required", ErrInvalidDomainValue)
	}
	if author, err := AuthorRole(entry.Stage); err == nil && entry.Role != author {
		if reviewer, reviewErr := ReviewerRole(entry.Stage); reviewErr != nil || entry.Role != reviewer {
			return fmt.Errorf("%w: role %s does not belong to stage %s", ErrInvalidDomainValue, entry.Role, entry.Stage)
		}
	}
	if stored && (entry.Sequence == 0 || len(entry.Checksum) != sha256.Size*2) {
		return fmt.Errorf("%w: stored mem-log sequence and checksum are required", ErrInvalidDomainValue)
	}
	if entry.StateHash != "" {
		if len(entry.StateHash) != sha256.Size*2 {
			return fmt.Errorf("%w: mem-log state hash is invalid", ErrInvalidDomainValue)
		}
		if _, err := hex.DecodeString(entry.StateHash); err != nil {
			return fmt.Errorf("%w: mem-log state hash is invalid", ErrInvalidDomainValue)
		}
	}
	return nil
}

const memLogHeaderPrefix = "# Feature memory log\n\nFeature: "

func newMemLog(featureID, brief string, at time.Time) ([]byte, []MemLogEntry, error) {
	return newMemLogBoundToState(featureID, brief, at, "")
}

func newMemLogBoundToState(featureID, brief string, at time.Time, stateHash string) ([]byte, []MemLogEntry, error) {
	if strings.TrimSpace(featureID) == "" {
		return nil, nil, fmt.Errorf("%w: feature ID is required", ErrInvalidDomainValue)
	}
	entry, err := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogBrief, at, brief)
	if err != nil {
		return nil, nil, err
	}
	entry.StateHash = stateHash
	data := []byte(memLogHeaderPrefix + featureID + "\n")
	data, entry, err = appendMemLogBytes(data, entry, "")
	if err != nil {
		return nil, nil, err
	}
	return data, []MemLogEntry{entry}, nil
}

func appendMemLogBytes(existing []byte, entry MemLogEntry, previous string) ([]byte, MemLogEntry, error) {
	if err := validateMemLogEntry(entry, false); err != nil {
		return nil, MemLogEntry{}, err
	}
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		return nil, MemLogEntry{}, fmt.Errorf("mem-log must end with a newline")
	}
	entry.Body = strings.TrimSpace(entry.Body)
	entry.Sequence++
	if entry.Sequence == 1 {
		previous = "root"
	}
	entry.At = entry.At.Round(0)
	entry.Checksum = memLogChecksum(entry, previous)

	var record strings.Builder
	fmt.Fprintf(&record, "\n## Entry %06d\n\n", entry.Sequence)
	fmt.Fprintf(&record, "Stage: %s\n", entry.Stage)
	fmt.Fprintf(&record, "Role: %s\n", entry.Role)
	fmt.Fprintf(&record, "Event: %s\n", entry.Kind)
	fmt.Fprintf(&record, "At: %s\n", entry.At.Format(time.RFC3339Nano))
	fmt.Fprintf(&record, "Previous: %s\n", previous)
	fmt.Fprintf(&record, "State-Hash: %s\n", entry.StateHash)
	fmt.Fprintf(&record, "Body-Length: %d\n", len([]byte(entry.Body)))
	fmt.Fprintf(&record, "Checksum: %s\n\n", entry.Checksum)
	record.WriteString(entry.Body)
	record.WriteByte('\n')
	return append(append([]byte(nil), existing...), []byte(record.String())...), entry, nil
}

func memLogChecksum(entry MemLogEntry, previous string) string {
	payload := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		entry.Sequence, entry.Stage, entry.Role, entry.Kind,
		entry.At.Format(time.RFC3339Nano), previous, entry.StateHash, entry.Body)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func parseMemLog(data []byte, featureID string) ([]MemLogEntry, error) {
	header := []byte(memLogHeaderPrefix + featureID + "\n")
	if !bytes.HasPrefix(data, header) {
		return nil, fmt.Errorf("mem-log header does not identify feature %s", featureID)
	}
	offset := len(header)
	entries := make([]MemLogEntry, 0)
	previous := "root"
	for offset < len(data) {
		if !bytes.HasPrefix(data[offset:], []byte("\n## Entry ")) {
			return nil, fmt.Errorf("mem-log contains bytes outside a typed entry at offset %d", offset)
		}
		offset++
		line, next, err := readLogLine(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		sequenceText := strings.TrimPrefix(line, "## Entry ")
		sequence, err := strconv.ParseUint(sequenceText, 10, 64)
		if err != nil || sequence != uint64(len(entries)+1) {
			return nil, fmt.Errorf("invalid mem-log entry sequence %q", sequenceText)
		}
		line, offset, err = readLogLine(data, offset)
		if err != nil || line != "" {
			return nil, fmt.Errorf("invalid mem-log entry separator")
		}

		fields := make(map[string]string, 8)
		for _, name := range []string{"Stage", "Role", "Event", "At", "Previous", "State-Hash", "Body-Length", "Checksum"} {
			line, next, err = readLogLine(data, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			prefix := name + ": "
			if !strings.HasPrefix(line, prefix) {
				return nil, fmt.Errorf("mem-log entry %d requires %s", sequence, name)
			}
			fields[name] = strings.TrimPrefix(line, prefix)
		}
		line, offset, err = readLogLine(data, offset)
		if err != nil || line != "" {
			return nil, fmt.Errorf("invalid mem-log body separator")
		}
		bodyLength, err := strconv.Atoi(fields["Body-Length"])
		if err != nil || bodyLength < 0 || offset+bodyLength >= len(data) {
			return nil, fmt.Errorf("invalid mem-log body length for entry %d", sequence)
		}
		body := string(data[offset : offset+bodyLength])
		offset += bodyLength
		if data[offset] != '\n' {
			return nil, fmt.Errorf("mem-log entry %d body is not newline terminated", sequence)
		}
		offset++
		at, err := time.Parse(time.RFC3339Nano, fields["At"])
		if err != nil {
			return nil, fmt.Errorf("invalid mem-log timestamp for entry %d: %w", sequence, err)
		}
		entry := MemLogEntry{
			Sequence: sequence, Stage: Stage(fields["Stage"]), Role: Role(fields["Role"]),
			Kind: MemLogEventKind(fields["Event"]), At: at, StateHash: fields["State-Hash"], Body: body, Checksum: fields["Checksum"],
		}
		if err := validateMemLogEntry(entry, true); err != nil {
			return nil, fmt.Errorf("mem-log entry %d: %w", sequence, err)
		}
		if fields["Previous"] != previous || memLogChecksum(entry, previous) != entry.Checksum {
			return nil, fmt.Errorf("mem-log entry %d checksum chain is invalid", sequence)
		}
		entries = append(entries, entry)
		previous = entry.Checksum
	}
	if len(entries) == 0 || entries[0].Kind != MemLogBrief {
		return nil, fmt.Errorf("mem-log has no feature brief")
	}
	return entries, nil
}

func readLogLine(data []byte, offset int) (string, int, error) {
	if offset >= len(data) {
		return "", offset, fmt.Errorf("unexpected end of mem-log")
	}
	end := bytes.IndexByte(data[offset:], '\n')
	if end < 0 {
		return "", offset, fmt.Errorf("unterminated mem-log line")
	}
	return string(data[offset : offset+end]), offset + end + 1, nil
}

// Journal is retained as the intent-only controller adapter until that
// controller is replaced. It writes the same typed format as FeatureRepository.
type Journal struct {
	path         string
	featureID    string
	nextDecision int
	now          func() time.Time
}

func NewJournal(path, datedID, brief string) (*Journal, error) {
	data, _, err := newMemLog(datedID, brief, time.Now())
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return &Journal{path: path, featureID: datedID, nextDecision: 1, now: time.Now}, nil
}

func (j *Journal) append(kind MemLogEventKind, text string) error {
	if j == nil {
		return fmt.Errorf("mem-log is unavailable")
	}
	data, err := os.ReadFile(j.path)
	if err != nil {
		return err
	}
	entries, err := parseMemLog(data, j.featureID)
	if err != nil {
		return err
	}
	entry, err := NewMemLogEntry(StageIntent, RoleIntentAuthor, kind, j.now(), text)
	if err != nil {
		return err
	}
	entry.Sequence = uint64(len(entries))
	updated, _, err := appendMemLogBytes(data, entry, entries[len(entries)-1].Checksum)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(updated[len(data):])
	return err
}

func (j *Journal) User(text string) error   { return j.append(MemLogUserMessage, text) }
func (j *Journal) Agent(text string) error  { return j.append(MemLogAgentMessage, text) }
func (j *Journal) Rework(text string) error { return j.append(MemLogRevisionDecision, "rework: "+text) }
func (j *Journal) Event(text string) error  { return j.append(MemLogSession, text) }

func (j *Journal) Decisions(values []Decision) error {
	if j == nil {
		return fmt.Errorf("mem-log is unavailable")
	}
	next := j.nextDecision
	for _, value := range values {
		for _, id := range value.Supersedes {
			if id <= 0 || id >= next {
				return fmt.Errorf("decision supersedes unknown or future decision %d", id)
			}
		}
		next++
	}
	if len(values) == 0 {
		return nil
	}
	var record strings.Builder
	next = j.nextDecision
	for _, value := range values {
		alternatives := "(none)"
		if len(value.Alternatives) > 0 {
			alternatives = strings.Join(value.Alternatives, "; ")
		}
		fmt.Fprintf(&record, "## Decision D-%03d\n\nauthor: %s\n\ndecision: %s\n\nrationale: %s\n\nalternatives: %s\n\nsupersedes: %v\n", next, value.Author, value.Decision, value.Rationale, alternatives, value.Supersedes)
		next++
	}
	if err := j.append(MemLogDecision, record.String()); err != nil {
		return err
	}
	j.nextDecision = next
	return nil
}
