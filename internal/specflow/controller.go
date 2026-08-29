package specflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type State uint8

const (
	StateIdle State = iota
	StateAwaitingBrief
	StateDialoguing
	StateIntentPublished
	StateAwaitingReview
	StateAwaitingRework
)

type Progress struct {
	State     State
	Path      string
	Message   string
	Diff      string
	FeatureID string
}
type ReviewAction string

const (
	ReviewApply  ReviewAction = "apply"
	ReviewReject ReviewAction = "reject"
	ReviewRework ReviewAction = "rework"
)

type dialogueRunner interface {
	StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error)
	RunTurn(agentruntime.Thread, string) (json.RawMessage, error)
	CloseThread(agentruntime.Thread) error
}
type Controller struct {
	runner    dialogueRunner
	root      string
	now       func() time.Time
	state     State
	thread    agentruntime.Thread
	artifact  string
	target    FeatureTarget
	journal   *Journal
	draftHash string
}

func NewController(root string, runner dialogueRunner) *Controller {
	return &Controller{root: root, runner: runner, now: time.Now}
}
func (c *Controller) Progress() Progress {
	return Progress{State: c.state, Path: c.target.DisplayIntentPath, FeatureID: c.target.ID}
}
func (c *Controller) StartFeature(brief string) (Progress, error) {
	c.reset()
	if c.runner == nil {
		return c.Progress(), fmt.Errorf("start feature: turn runner is required")
	}
	if strings.TrimSpace(brief) == "" {
		c.state = StateAwaitingBrief
		return c.Progress(), nil
	}
	return c.start(brief)
}
func (c *Controller) StartIdea(brief string) (Progress, error) { return c.StartFeature(brief) }
func (c *Controller) Submit(text string) (Progress, error) {
	switch c.state {
	case StateAwaitingBrief:
		if strings.TrimSpace(text) == "" {
			return c.Progress(), fmt.Errorf("feature brief must not be empty")
		}
		return c.start(text)
	case StateDialoguing, StateIntentPublished:
		if text == "/approve" {
			return c.Approve()
		}
		return c.turn(text, "message")
	case StateAwaitingRework:
		if strings.TrimSpace(text) == "" {
			return c.Progress(), fmt.Errorf("rework comment must not be empty")
		}
		if err := c.journal.Rework(text); err != nil {
			return c.fail(err)
		}
		return c.turn(text, "rework")
	default:
		return c.Progress(), fmt.Errorf("intent dialogue is not awaiting input")
	}
}
func (c *Controller) Review(action ReviewAction) (Progress, error) {
	if c.state != StateAwaitingReview {
		return c.Progress(), fmt.Errorf("no intent revision is awaiting review")
	}
	switch action {
	case ReviewApply:
		data, err := c.readDraft(true)
		if err != nil {
			return c.fail(err)
		}
		if hash(data) != c.draftHash {
			return c.fail(fmt.Errorf("reviewed draft changed before apply"))
		}
		if err = c.writeAtomic(c.target.IntentPath, data); err != nil {
			return c.fail(err)
		}
		if err = c.journal.Event("revision applied"); err != nil {
			return c.fail(err)
		}
		c.draftHash = hash(data)
		c.state = StateIntentPublished
		return c.Progress(), nil
	case ReviewReject:
		if err := c.journal.Event("revision rejected"); err != nil {
			return c.fail(err)
		}
		c.state = StateIntentPublished
		return c.Progress(), nil
	case ReviewRework:
		if err := c.journal.Event("revision rework requested"); err != nil {
			return c.fail(err)
		}
		c.state = StateAwaitingRework
		return c.Progress(), nil
	default:
		return c.Progress(), fmt.Errorf("unknown review action %q", action)
	}
}
func (c *Controller) Approve() (Progress, error) {
	if c.state != StateIntentPublished {
		return c.Progress(), fmt.Errorf("/approve is available only after publishing intent.md")
	}
	info, err := os.Lstat(c.target.IntentPath)
	if err != nil || !info.Mode().IsRegular() {
		return c.Progress(), fmt.Errorf("approve intent: %w", err)
	}
	if err := c.journal.Event("intent approved"); err != nil {
		return c.fail(err)
	}
	if err := c.closeThread(); err != nil {
		return c.fail(err)
	}
	if err := removeArtifact(c.artifact); err != nil {
		return c.fail(err)
	}
	c.reset()
	return c.Progress(), nil
}

func (c *Controller) start(brief string) (Progress, error) {
	idThread, err := c.runner.StartThread(agentruntime.ThreadConfig{Workspace: c.root, OutputSchema: FeatureIDSchema()})
	if err != nil {
		return c.Progress(), fmt.Errorf("start feature-id request: %w", err)
	}
	raw, err := c.runner.RunTurn(idThread, FeatureIDPrompt(brief))
	closeIDErr := c.runner.CloseThread(idThread)
	if err != nil {
		return c.Progress(), fmt.Errorf("generate feature-id: %w", err)
	}
	if closeIDErr != nil {
		return c.Progress(), fmt.Errorf("close feature-id thread: %w", closeIDErr)
	}
	id, err := DecodeFeatureID(raw)
	if err != nil {
		return c.Progress(), fmt.Errorf("validate feature-id: %w", err)
	}
	target, err := PrepareFeatureTarget(c.root, c.now().Format("2006-01-02"), id.FeatureID)
	if err != nil {
		return c.Progress(), fmt.Errorf("prepare feature target: %w", err)
	}
	if err := os.Mkdir(target.Directory, 0o755); err != nil {
		return c.Progress(), fmt.Errorf("create feature directory: %w", err)
	}
	journal, err := NewJournal(target.JournalPath, target.ID, brief)
	if err != nil {
		return c.fail(fmt.Errorf("create mem-log: %w", err))
	}
	c.target, c.journal = target, journal
	if err := c.journal.Event("dated feature ID selected: " + target.ID); err != nil {
		return c.fail(err)
	}
	artifact, err := CreateArtifactRoot(c.root)
	if err != nil {
		return c.fail(fmt.Errorf("create artifact root: %w", err))
	}
	c.artifact = artifact
	thread, err := c.runner.StartThread(agentruntime.ThreadConfig{BootstrapInstructions: BootstrapPrompt(brief, artifact), OutputSchema: DialogueSchema(), Workspace: c.root, ArtifactRoot: artifact})
	if err != nil {
		return c.fail(fmt.Errorf("start intent thread: %w", err))
	}
	c.thread = thread
	c.state = StateDialoguing
	return c.run(brief)
}
func (c *Controller) turn(text, _ string) (Progress, error) {
	if err := c.journal.User(text); err != nil {
		return c.fail(err)
	}
	return c.run(text)
}
func (c *Controller) run(input string) (Progress, error) {
	before, _ := c.draftFingerprint()
	raw, err := c.runner.RunTurn(c.thread, input)
	if err != nil {
		return c.fail(fmt.Errorf("run intent turn: %w", err))
	}
	envelope, err := DecodeEnvelope(raw)
	if err != nil {
		return c.fail(fmt.Errorf("validate intent response: %w", err))
	}
	if err := c.journal.Decisions(envelope.Decisions); err != nil {
		return c.fail(err)
	}
	if envelope.Kind == KindMessage {
		if err := c.journal.Agent(envelope.Message); err != nil {
			return c.fail(err)
		}
		p := c.Progress()
		p.Message = envelope.Message
		return p, nil
	}
	data, err := c.readDraftChanged(before)
	if err != nil {
		return c.fail(err)
	}
	if _, err := os.Stat(c.target.IntentPath); os.IsNotExist(err) {
		if err := c.writeAtomic(c.target.IntentPath, data); err != nil {
			return c.fail(err)
		}
		if err := c.journal.Event("first draft published: " + c.target.DisplayIntentPath); err != nil {
			return c.fail(err)
		}
		c.draftHash = hash(data)
		c.state = StateIntentPublished
		return c.Progress(), nil
	}
	published, err := os.ReadFile(c.target.IntentPath)
	if err != nil {
		return c.fail(err)
	}
	c.draftHash = hash(data)
	c.state = StateAwaitingReview
	p := c.Progress()
	p.Diff = unifiedDiff(published, data)
	if err := c.journal.Event("revision reviewed\n\n" + p.Diff); err != nil {
		return c.fail(err)
	}
	return p, nil
}
func (c *Controller) draftFingerprint() (string, error) {
	data, err := c.readDraft(false)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash(data), nil
}
func (c *Controller) readDraftChanged(before string) ([]byte, error) {
	data, err := c.readDraft(true)
	if err != nil {
		return nil, err
	}
	if before == hash(data) {
		return nil, fmt.Errorf("draft intent.md was not created or changed in this turn")
	}
	return data, nil
}
func (c *Controller) readDraft(_ bool) ([]byte, error) {
	p := filepath.Join(c.artifact, "intent.md")
	if c.artifact == "" {
		return nil, fmt.Errorf("artifact root is unavailable")
	}
	if !withinPath(c.artifact, p) {
		return nil, fmt.Errorf("draft escapes artifact root")
	}
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("draft must be a regular non-link file")
	}
	return os.ReadFile(p)
}
func (c *Controller) writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".intent-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (c *Controller) fail(err error) (Progress, error) {
	if c.journal != nil {
		_ = c.journal.Event("flow error: " + err.Error())
	}
	_ = c.closeThread()
	_ = removeArtifact(c.artifact)
	c.reset()
	return c.Progress(), err
}

// Cancel performs the same durable cleanup as an expected user interruption.
// It deliberately preserves published project artifacts and only removes the
// external draft root.
func (c *Controller) Cancel() {
	if c.journal != nil {
		_ = c.journal.Event("flow canceled")
	}
	_ = c.closeThread()
	_ = removeArtifact(c.artifact)
	c.reset()
}
func (c *Controller) closeThread() error {
	if c.thread == nil || c.runner == nil {
		return nil
	}
	err := c.runner.CloseThread(c.thread)
	c.thread = nil
	return err
}
func (c *Controller) reset() {
	c.state = StateIdle
	c.thread = nil
	c.artifact = ""
	c.target = FeatureTarget{}
	c.journal = nil
	c.draftHash = ""
}
func withinPath(root, value string) bool {
	r, err := filepath.Rel(root, value)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func removeArtifact(root string) error {
	if root == "" {
		return nil
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if filepath.Clean(canonical) != filepath.Clean(root) {
		return fmt.Errorf("refuse cleanup through link")
	}
	return os.RemoveAll(root)
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func unifiedDiff(old, new []byte) string {
	before, after := diffLines(string(old)), diffLines(string(new))
	type operation struct {
		kind byte
		line string
	}
	// A compact LCS implementation is sufficient here: intent files are small,
	// and it gives users actual hunks with unchanged context rather than a full
	// delete/add replacement.
	table := make([][]int, len(before)+1)
	for i := range table {
		table[i] = make([]int, len(after)+1)
	}
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i][j] = 1 + table[i+1][j+1]
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	var ops []operation
	for i, j := 0, 0; i < len(before) || j < len(after); {
		switch {
		case i < len(before) && j < len(after) && before[i] == after[j]:
			ops = append(ops, operation{' ', before[i]})
			i++
			j++
		case j < len(after) && (i == len(before) || table[i][j+1] >= table[i+1][j]):
			ops = append(ops, operation{'+', after[j]})
			j++
		default:
			ops = append(ops, operation{'-', before[i]})
			i++
		}
	}
	var output strings.Builder
	output.WriteString("--- intent.md\n+++ intent.md\n")
	for start := 0; start < len(ops); {
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start == len(ops) {
			break
		}
		hunkStart := start - 3
		if hunkStart < 0 {
			hunkStart = 0
		}
		end := start + 1
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			contextEnd := end
			for contextEnd < len(ops) && ops[contextEnd].kind == ' ' {
				contextEnd++
			}
			if contextEnd-end > 6 {
				end += 3
				break
			}
			end = contextEnd
		}
		oldStart, newStart := 1, 1
		for _, op := range ops[:hunkStart] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[hunkStart:end] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[hunkStart:end] {
			output.WriteByte(op.kind)
			output.WriteString(op.line)
			output.WriteByte('\n')
		}
		start = end
	}
	return output.String()
}

func diffLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}
