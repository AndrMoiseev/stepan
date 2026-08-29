package specflow

import (
	"fmt"
	"os"
	"strings"
)

// Journal is the sole writer of mem-log.md. Every operation appends one
// complete logical record, so previous bytes are never rewritten.
type Journal struct {
	path         string
	nextDecision int
}

func NewJournal(path, datedID, brief string) (*Journal, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	journal := &Journal{path: path, nextDecision: 1}
	if _, err = file.WriteString("# Intent memory log\n\n## Feature\n\n" + datedID + "\n\n## User brief\n\n" + brief + "\n"); err != nil {
		return nil, err
	}
	return journal, nil
}
func (j *Journal) append(kind, text string) error {
	if j == nil {
		return fmt.Errorf("mem-log is unavailable")
	}
	file, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString("\n## " + kind + "\n\n" + text + "\n")
	return err
}
func (j *Journal) User(text string) error   { return j.append("User", text) }
func (j *Journal) Agent(text string) error  { return j.append("Agent", text) }
func (j *Journal) Rework(text string) error { return j.append("Rework comment", text) }
func (j *Journal) Event(text string) error  { return j.append("Event", text) }
func (j *Journal) Decisions(values []Decision) error {
	for _, value := range values {
		for _, id := range value.Supersedes {
			if id <= 0 || id >= j.nextDecision {
				return fmt.Errorf("decision supersedes unknown or future decision %d", id)
			}
		}
		alternatives := "(none)"
		if len(value.Alternatives) > 0 {
			alternatives = strings.Join(value.Alternatives, "; ")
		}
		if err := j.append(fmt.Sprintf("Decision D-%03d", j.nextDecision), fmt.Sprintf("author: %s\n\ndecision: %s\n\nrationale: %s\n\nalternatives: %s\n\nsupersedes: %v", value.Author, value.Decision, value.Rationale, alternatives, value.Supersedes)); err != nil {
			return err
		}
		j.nextDecision++
	}
	return nil
}
