package specflow

func (v *documentValidation) validateSpec() {
	activeRequirements := make(map[StableID]struct{})
	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() {
			continue
		}
		v.validateFieldNamesAndDuplicates(heading)
		switch heading.id.Family() {
		case IDRequirement:
			activeRequirements[heading.id] = struct{}{}
		case IDDecision, IDAcceptance:
		default:
			v.add(DiagnosticDisallowedID, heading.line, heading.idSpelling, "spec may declare only REQ, DEC, and AC headings")
		}
	}

	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() || heading.id.Family() != IDAcceptance {
			continue
		}
		traces, present := parseReferenceField(v, heading, "Traces")
		if !present || len(traces) == 0 {
			v.add(DiagnosticMissingTraces, heading.line, heading.idSpelling, "acceptance criterion must trace at least one requirement")
		}
		for _, trace := range traces {
			traceLine := fieldLine(heading, "Traces")
			if trace.Family() != IDRequirement {
				v.add(DiagnosticInvalidTraceFamily, traceLine, heading.idSpelling, "acceptance criterion may trace only REQ identities")
				continue
			}
			v.referenceExists(trace, activeRequirements, traceLine, heading.idSpelling)
		}
		v.result.Document.Elements = append(v.result.Document.Elements, DocumentElement{
			ID: heading.id, Spelling: heading.idSpelling, Title: heading.title,
			Line: heading.line, Level: heading.level, Traces: cloneStableIDs(traces),
		})
	}

	// Preserve source order while including elements that do not carry Traces.
	withAcceptance := make(map[StableID]DocumentElement)
	for _, element := range v.result.Document.Elements {
		withAcceptance[element.ID] = element
	}
	v.result.Document.Elements = v.result.Document.Elements[:0]
	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() {
			continue
		}
		if heading.id.Family() != IDRequirement && heading.id.Family() != IDDecision && heading.id.Family() != IDAcceptance {
			continue
		}
		if element, ok := withAcceptance[heading.id]; ok {
			v.result.Document.Elements = append(v.result.Document.Elements, element)
			continue
		}
		v.result.Document.Elements = append(v.result.Document.Elements, DocumentElement{
			ID: heading.id, Spelling: heading.idSpelling, Title: heading.title,
			Line: heading.line, Level: heading.level,
		})
	}
}
