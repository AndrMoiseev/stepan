package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

// ErrBriefVersion identifies a controller boundary that cannot safely publish
// or consume an assignment brief.
var ErrBriefVersion = errors.New("invalid assignment brief version")

// PersistBriefVersion creates the next immutable Markdown contract for an
// active assignment. The controller, rather than the briefer, owns every
// frontmatter field. Publish and verification both complete before the state
// event which first references the document is written.
func PersistBriefVersion(ctx context.Context, journal *runstore.Run, stateStore *runstore.StateStore, run *implstate.Run, assignmentID implstate.AssignmentID, taskIDs []implstate.TaskID, content string) (implstate.BriefVersion, error) {
	assignment, err := activeBriefAssignment(run, assignmentID)
	if err != nil {
		return implstate.BriefVersion{}, err
	}
	if !slices.Equal(taskIDs, assignment.TaskIDs) || strings.TrimSpace(content) == "" {
		return implstate.BriefVersion{}, fmt.Errorf("%w: brief content and the stable assignment task IDs are required", ErrBriefVersion)
	}
	if journal == nil || stateStore == nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: journal and state store are required", ErrBriefVersion)
	}

	number := len(assignment.Briefs) + 1
	briefID := implstate.BriefID(fmt.Sprintf("brief-%s-v%d", assignmentID, number))
	documentID := implstate.EvidenceID(fmt.Sprintf("brief-%s-v%d.md", assignmentID, number))
	document, err := journal.Publish(documentID, renderBriefDocument(assignmentID, number, assignment.TaskIDs, content))
	if err != nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: publish Markdown document: %v", ErrBriefVersion, err)
	}
	// Verify now rather than relying solely on StateStore.Record. This preserves
	// the file-publication boundary even if another StateStore implementation is
	// introduced later.
	if err := journal.VerifyReference(document); err != nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: verify published Markdown document: %v", ErrBriefVersion, err)
	}
	brief := implstate.BriefVersion{ID: briefID, Number: number, Document: document}
	if err := run.AddBriefVersion(assignmentID, brief); err != nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: attach brief version: %v", ErrBriefVersion, err)
	}
	if _, err := stateStore.Record(ctx, run); err != nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: record brief reference: %v", ErrBriefVersion, err)
	}
	return brief, nil
}

// PersistRefinedBriefVersion accepts only a briefer response bound to the
// assignment's current brief. Refinement cannot replace the selected task
// block; it can only create its next content version.
func PersistRefinedBriefVersion(ctx context.Context, journal *runstore.Run, stateStore *runstore.StateStore, run *implstate.Run, expectation ResponseExpectation, response AgentResponse) (implstate.BriefVersion, error) {
	assignment, err := activeBriefAssignment(run, expectation.Binding.AssignmentID)
	if err != nil {
		return implstate.BriefVersion{}, err
	}
	if expectation.Role != ResponseRoleBriefer || expectation.State != ResponseStateBriefRefinement || expectation.Scope != ResponseScopeAssignment || response.Kind != ResponseBriefReady || response.Binding != expectation.Binding || expectation.Binding.RunID != run.Identity.ID || len(assignment.Briefs) == 0 || expectation.Binding.BriefID != assignment.Briefs[len(assignment.Briefs)-1].ID || response.Brief == nil {
		return implstate.BriefVersion{}, fmt.Errorf("%w: response is not bound to the current assignment brief", ErrBriefVersion)
	}
	return PersistBriefVersion(ctx, journal, stateStore, run, assignment.ID, response.TaskIDs, *response.Brief)
}

// BuildCurrentTaskRoleContexts reads and validates the controller-published
// current version once, then derives both task-role contexts from that exact
// value. Rules are deliberately supplied live and never copied into a brief
// artifact or versioned with it.
func BuildCurrentTaskRoleContexts(journal *runstore.Run, run *implstate.Run, assignmentID implstate.AssignmentID, rules RulesIndex, checks []CheckCatalogEntry) (RoleStartContext, RoleStartContext, error) {
	assignment, err := activeBriefAssignment(run, assignmentID)
	if err != nil {
		return RoleStartContext{}, RoleStartContext{}, err
	}
	if journal == nil || len(assignment.Briefs) == 0 {
		return RoleStartContext{}, RoleStartContext{}, fmt.Errorf("%w: current brief and journal are required", ErrBriefVersion)
	}
	brief := assignment.Briefs[len(assignment.Briefs)-1]
	document, err := journal.Read(brief.Document)
	if err != nil {
		return RoleStartContext{}, RoleStartContext{}, fmt.Errorf("%w: read current brief: %v", ErrBriefVersion, err)
	}
	if _, err := parseBriefDocument(document, assignment.ID, brief.Number, assignment.TaskIDs); err != nil {
		return RoleStartContext{}, RoleStartContext{}, err
	}
	// Keep the artifact intact after validation. The hash-bound Markdown,
	// including controller-generated YAML metadata, is the contract both roles
	// must receive; passing only its body would silently detach their context
	// from the durable version they are asked to implement or review.
	input := TaskRoleStartInput{AssignmentID: assignment.ID, BriefID: brief.ID, Brief: string(document), Rules: rules, Checks: checks}
	implementer, err := BuildImplementerStartContext(input)
	if err != nil {
		return RoleStartContext{}, RoleStartContext{}, err
	}
	reviewer, err := BuildTaskReviewerStartContext(input)
	if err != nil {
		return RoleStartContext{}, RoleStartContext{}, err
	}
	return implementer, reviewer, nil
}

func activeBriefAssignment(run *implstate.Run, assignmentID implstate.AssignmentID) (*implstate.Assignment, error) {
	if run == nil || assignmentID == "" {
		return nil, fmt.Errorf("%w: active assignment is required", ErrBriefVersion)
	}
	for index := range run.Assignments {
		assignment := &run.Assignments[index]
		if assignment.ID == assignmentID && assignment.Status == implstate.AssignmentActive {
			return assignment, nil
		}
	}
	return nil, fmt.Errorf("%w: assignment is not active", ErrBriefVersion)
}

func renderBriefDocument(assignmentID implstate.AssignmentID, version int, taskIDs []implstate.TaskID, content string) []byte {
	var document strings.Builder
	document.WriteString("---\nassignment_id: ")
	document.WriteString(yamlScalar(string(assignmentID)))
	document.WriteString("\nversion: ")
	document.WriteString(strconv.Itoa(version))
	document.WriteString("\ntask_ids:\n")
	for _, taskID := range taskIDs {
		document.WriteString("  - ")
		document.WriteString(yamlScalar(string(taskID)))
		document.WriteByte('\n')
	}
	document.WriteString("---\n\n")
	document.WriteString(strings.TrimSpace(content))
	document.WriteByte('\n')
	return []byte(document.String())
}

func parseBriefDocument(document []byte, assignmentID implstate.AssignmentID, version int, taskIDs []implstate.TaskID) (string, error) {
	lines := strings.Split(string(document), "\n")
	if len(lines) < 6 || lines[0] != "---" || lines[1] != "assignment_id: "+yamlScalar(string(assignmentID)) || lines[2] != "version: "+strconv.Itoa(version) || lines[3] != "task_ids:" {
		return "", fmt.Errorf("%w: controller frontmatter is missing or mismatched", ErrBriefVersion)
	}
	line := 4
	for _, taskID := range taskIDs {
		if line >= len(lines) || lines[line] != "  - "+yamlScalar(string(taskID)) {
			return "", fmt.Errorf("%w: controller task IDs are missing or mismatched", ErrBriefVersion)
		}
		line++
	}
	if line >= len(lines) || lines[line] != "---" {
		return "", fmt.Errorf("%w: controller frontmatter is incomplete", ErrBriefVersion)
	}
	body := strings.TrimSpace(strings.Join(lines[line+1:], "\n"))
	if body == "" {
		return "", fmt.Errorf("%w: brief body is empty", ErrBriefVersion)
	}
	return body, nil
}

func yamlScalar(value string) string {
	if value != "" {
		for _, character := range value {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
				return strconv.Quote(value)
			}
		}
		return value
	}
	return strconv.Quote(value)
}
