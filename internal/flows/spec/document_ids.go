package specflow

import (
	"regexp"
	"strings"
)

var (
	atxHeadingPattern  = regexp.MustCompile(`^(#{1,6})[\t ]+(.+?)[\t ]*#*[\t ]*$`)
	fieldPattern       = regexp.MustCompile(`^[\t ]*(?:[-*][\t ]+)?([A-Za-z][A-Za-z-]*):[\t ]*(.*)$`)
	idCandidatePattern = regexp.MustCompile(`(?i)^(REQ|DEC|AC|TASK|SPEC-F|PLAN-F)-`)
)

var canonicalFieldNames = map[string]string{
	"traces":         "Traces",
	"depends-on":     "Depends-on",
	"severity":       "Severity",
	"status":         "Status",
	"problem":        "Problem",
	"location":       "Location",
	"recommendation": "Recommendation",
	"resolution":     "Resolution",
	"superseded-by":  "Superseded-by",
	"decision":       "Decision",
	"decided-by":     "Decided-by",
	"rationale":      "Rationale",
}

type rawField struct {
	name     string
	spelling string
	value    string
	line     int
}

type rawHeading struct {
	index         int
	line          int
	level         int
	title         string
	parent        int
	idCandidate   bool
	idSpelling    string
	id            StableID
	openQuestions bool
	testScenario  bool
	fields        map[string][]rawField
}

type parsedMarkdown struct {
	lines    []string
	headings []rawHeading
}

func parseMarkdown(source string) parsedMarkdown {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	result := parsedMarkdown{lines: lines}
	stack := make([]int, 0, 6)
	inFence := false
	var fenceChar byte
	var fenceLen int

	for lineIndex, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		if indent <= 3 {
			if marker, count, ok := fenceMarker(trimmed); ok {
				if !inFence {
					inFence, fenceChar, fenceLen = true, marker, count
				} else if marker == fenceChar && count >= fenceLen {
					inFence = false
				}
				continue
			}
		}
		if inFence {
			continue
		}

		if match := atxHeadingPattern.FindStringSubmatch(line); match != nil {
			level := len(match[1])
			for len(stack) > 0 && result.headings[stack[len(stack)-1]].level >= level {
				stack = stack[:len(stack)-1]
			}
			parent := -1
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			title := strings.TrimSpace(match[2])
			heading := rawHeading{
				index: len(result.headings), line: lineIndex + 1, level: level,
				title: title, parent: parent, fields: make(map[string][]rawField),
				openQuestions: title == "Open questions",
				testScenario:  strings.HasPrefix(title, "Test scenario"),
			}
			heading.idCandidate, heading.idSpelling, heading.id = parseHeadingStableID(title)
			result.headings = append(result.headings, heading)
			stack = append(stack, heading.index)
			continue
		}

		match := fieldPattern.FindStringSubmatch(line)
		if match == nil || len(stack) == 0 {
			continue
		}
		canonical, known := canonicalFieldNames[strings.ToLower(match[1])]
		if !known {
			continue
		}
		owner := nearestMachineHeading(result.headings, stack)
		if owner < 0 {
			continue
		}
		result.headings[owner].fields[canonical] = append(result.headings[owner].fields[canonical], rawField{
			name: canonical, spelling: match[1], value: strings.TrimSpace(match[2]), line: lineIndex + 1,
		})
	}
	return result
}

func fenceMarker(line string) (byte, int, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	return marker, count, count >= 3
}

func parseHeadingStableID(title string) (bool, string, StableID) {
	token := title
	if index := strings.IndexAny(token, " \t—–"); index >= 0 {
		token = token[:index]
	}
	if !idCandidatePattern.MatchString(token) {
		return false, "", StableID{}
	}
	id, err := ParseStableID(token)
	if err != nil {
		return true, token, StableID{}
	}
	return true, token, id
}

func nearestMachineHeading(headings []rawHeading, stack []int) int {
	for i := len(stack) - 1; i >= 0; i-- {
		heading := headings[stack[i]]
		if heading.testScenario || heading.id.Valid() || heading.idCandidate {
			return heading.index
		}
	}
	return -1
}

func (p parsedMarkdown) sectionHasContent(index int) bool {
	heading := p.headings[index]
	endLine := len(p.lines) + 1
	for next := index + 1; next < len(p.headings); next++ {
		if p.headings[next].level <= heading.level {
			endLine = p.headings[next].line
			break
		}
	}
	for line := heading.line + 1; line < endLine; line++ {
		if strings.TrimSpace(p.lines[line-1]) != "" {
			return true
		}
	}
	return false
}

func (p parsedMarkdown) eofLine() int {
	if len(p.lines) == 0 {
		return 1
	}
	return len(p.lines)
}

func (v *documentValidation) validateFieldNamesAndDuplicates(heading rawHeading) {
	for _, name := range orderedFieldNames() {
		fields := heading.fields[name]
		for _, field := range fields {
			if field.spelling != name {
				v.add(DiagnosticInvalidFieldName, field.line, field.spelling, "field must be spelled exactly as "+name+":")
			}
		}
		if len(fields) > 1 {
			for _, field := range fields[1:] {
				v.add(DiagnosticDuplicateField, field.line, heading.idSpelling, "field "+name+" appears more than once")
			}
		}
	}
}

func orderedFieldNames() []string {
	return []string{"Traces", "Depends-on", "Severity", "Status", "Problem", "Location", "Recommendation", "Resolution", "Superseded-by", "Decision", "Decided-by", "Rationale"}
}

func firstField(heading rawHeading, name string) (rawField, bool) {
	fields := heading.fields[name]
	if len(fields) == 0 {
		return rawField{}, false
	}
	return fields[0], true
}

func parseReferenceField(v *documentValidation, heading rawHeading, name string) ([]StableID, bool) {
	fields := heading.fields[name]
	if len(fields) == 0 {
		return nil, false
	}
	result := make([]StableID, 0)
	seen := make(map[StableID]struct{})
	for _, field := range fields {
		pieces := strings.FieldsFunc(field.value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		})
		for _, piece := range pieces {
			spelling := strings.Trim(piece, "`")
			id, err := ParseStableID(spelling)
			if err != nil {
				v.add(DiagnosticInvalidReference, field.line, heading.idSpelling, "invalid "+name+" reference "+piece)
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result, true
}

func (v *documentValidation) referenceExists(id StableID, active map[StableID]struct{}, line int, subject string) bool {
	if _, ok := active[id]; ok {
		return true
	}
	if _, wasIssued := v.issued[id]; wasIssued {
		v.add(DiagnosticDeletedReference, line, subject, "reference "+id.String()+" names an issued but inactive element")
	} else {
		v.add(DiagnosticUnknownReference, line, subject, "reference "+id.String()+" does not exist")
	}
	return false
}

func headingAncestor(headings []rawHeading, heading rawHeading, predicate func(rawHeading) bool) (rawHeading, bool) {
	for parent := heading.parent; parent >= 0; parent = headings[parent].parent {
		candidate := headings[parent]
		if predicate(candidate) {
			return candidate, true
		}
	}
	return rawHeading{}, false
}
