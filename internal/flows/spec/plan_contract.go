package specflow

import "fmt"

func (v *documentValidation) validatePlan() {
	tasks := make(map[StableID]rawHeading)
	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() {
			continue
		}
		v.validateFieldNamesAndDuplicates(heading)
		if heading.id.Family() != IDTask {
			v.add(DiagnosticDisallowedID, heading.line, heading.idSpelling, "plan may declare only TASK headings")
			continue
		}
		if _, duplicate := tasks[heading.id]; !duplicate {
			tasks[heading.id] = heading
		}
	}

	taskTraceCoverage := make(map[StableID]struct{})
	scenarioCoverage := make(map[StableID]struct{})
	scenariosByTask := make(map[StableID]int)
	graph := make(map[StableID][]StableID, len(tasks))

	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() || heading.id.Family() != IDTask {
			continue
		}
		traces, present := parseReferenceField(v, heading, "Traces")
		if !present || len(traces) == 0 {
			v.add(DiagnosticMissingTraces, heading.line, heading.idSpelling, "task must trace at least one REQ or DEC identity")
		}
		hasRequiredFamily := false
		for _, trace := range traces {
			traceLine := fieldLine(heading, "Traces")
			switch trace.Family() {
			case IDRequirement, IDDecision:
				hasRequiredFamily = true
			case IDAcceptance:
			default:
				v.add(DiagnosticInvalidTraceFamily, traceLine, heading.idSpelling, "task traces may contain only REQ, DEC, and optional AC identities")
				continue
			}
			if v.referenceExists(trace, v.active, traceLine, heading.idSpelling) {
				taskTraceCoverage[trace] = struct{}{}
			}
		}
		if present && len(traces) > 0 && !hasRequiredFamily {
			v.add(DiagnosticMissingTraces, heading.line, heading.idSpelling, "task traces must include at least one REQ or DEC identity")
		}

		dependencies, _ := parseReferenceField(v, heading, "Depends-on")
		for _, dependency := range dependencies {
			if dependency.Family() != IDTask {
				v.add(DiagnosticInvalidDependency, fieldLine(heading, "Depends-on"), heading.idSpelling, "Depends-on may contain only TASK identities")
				continue
			}
			if dependency == heading.id {
				v.add(DiagnosticSelfDependency, fieldLine(heading, "Depends-on"), heading.idSpelling, "task cannot depend on itself")
				continue
			}
			if _, exists := tasks[dependency]; !exists {
				v.referenceExists(dependency, taskIDSet(tasks), fieldLine(heading, "Depends-on"), heading.idSpelling)
				continue
			}
			graph[heading.id] = append(graph[heading.id], dependency)
		}

		v.result.Document.Elements = append(v.result.Document.Elements, DocumentElement{
			ID: heading.id, Spelling: heading.idSpelling, Title: heading.title,
			Line: heading.line, Level: heading.level, Traces: cloneStableIDs(traces),
			DependsOn: cloneStableIDs(dependencies),
		})
	}

	for _, heading := range v.parsed.headings {
		if !heading.testScenario {
			continue
		}
		v.validateFieldNamesAndDuplicates(heading)
		task, nested := headingAncestor(v.parsed.headings, heading, func(candidate rawHeading) bool {
			return candidate.id.Valid() && candidate.id.Family() == IDTask
		})
		if !nested {
			v.add(DiagnosticOrphanTestScenario, heading.line, heading.title, "Test scenario must be nested under a TASK heading")
		}
		traces, present := parseReferenceField(v, heading, "Traces")
		if !present || len(traces) == 0 {
			v.add(DiagnosticMissingTraces, heading.line, heading.title, "Test scenario must trace at least one AC or REQ identity")
		}
		for _, trace := range traces {
			traceLine := fieldLine(heading, "Traces")
			if trace.Family() != IDAcceptance && trace.Family() != IDRequirement {
				v.add(DiagnosticInvalidTraceFamily, traceLine, heading.title, "Test scenario may trace only AC or REQ identities")
				continue
			}
			if v.referenceExists(trace, v.active, traceLine, heading.title) {
				scenarioCoverage[trace] = struct{}{}
			}
		}
		taskID := StableID{}
		if nested {
			taskID = task.id
			scenariosByTask[task.id]++
		}
		v.result.Document.TestScenarios = append(v.result.Document.TestScenarios, ParsedTestScenario{
			Title: heading.title, Line: heading.line, Level: heading.level,
			TaskID: taskID, Traces: cloneStableIDs(traces),
		})
	}

	for id, heading := range tasks {
		if scenariosByTask[id] == 0 {
			v.add(DiagnosticMissingTestScenario, heading.line, heading.idSpelling, "task must contain at least one nested Test scenario heading")
		}
	}
	v.validateTaskCycles(tasks, graph)
	v.validatePlanCoverage(taskTraceCoverage, scenarioCoverage)
}

func taskIDSet(tasks map[StableID]rawHeading) map[StableID]struct{} {
	result := make(map[StableID]struct{}, len(tasks))
	for id := range tasks {
		result[id] = struct{}{}
	}
	return result
}

func (v *documentValidation) validateTaskCycles(tasks map[StableID]rawHeading, graph map[StableID][]StableID) {
	for id, heading := range tasks {
		if reachesTask(id, id, graph, make(map[StableID]bool)) {
			v.add(DiagnosticDependencyCycle, heading.line, heading.idSpelling, "task participates in a dependency cycle")
		}
	}
}

func reachesTask(current, target StableID, graph map[StableID][]StableID, visiting map[StableID]bool) bool {
	if visiting[current] {
		return false
	}
	visiting[current] = true
	defer delete(visiting, current)
	for _, next := range graph[current] {
		if next == target || reachesTask(next, target, graph, visiting) {
			return true
		}
	}
	return false
}

func (v *documentValidation) validatePlanCoverage(taskCoverage, scenarioCoverage map[StableID]struct{}) {
	line := v.parsed.eofLine()
	for _, id := range sortedStableIDs(v.active) {
		switch id.Family() {
		case IDRequirement, IDDecision:
			if _, covered := taskCoverage[id]; !covered {
				v.add(DiagnosticUncoveredElement, line, id.String(), fmt.Sprintf("active %s is not covered by a task Traces field", id))
			}
		case IDAcceptance:
			if _, covered := scenarioCoverage[id]; !covered {
				v.add(DiagnosticUncoveredElement, line, id.String(), fmt.Sprintf("active %s is not covered by a Test scenario", id))
			}
		}
	}
}

func sortedStableIDs(values map[StableID]struct{}) []StableID {
	result := make([]StableID, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sortStableIDs(result)
	return result
}

func sortStableIDs(values []StableID) {
	// Numeric suffixes are arbitrary precision strings, so compare by family,
	// digit count, then lexical digits instead of converting to an integer.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && stableIDLess(values[j], values[j-1]); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func stableIDLess(left, right StableID) bool {
	if left.Family() != right.Family() {
		return left.Family() < right.Family()
	}
	if len(left.Number()) != len(right.Number()) {
		return len(left.Number()) < len(right.Number())
	}
	return left.Number() < right.Number()
}
