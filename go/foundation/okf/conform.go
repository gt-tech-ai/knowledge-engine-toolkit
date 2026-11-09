package okf

import (
	pathpkg "path"
	"strings"
	"time"
)

// Reserved file names (OKF §3.1).
const (
	// IndexFile is the directory listing.
	IndexFile = "index.md"

	// LogFile is the dated update history.
	LogFile = "log.md"
)

// Conform reports where doc breaks the OKF structure (§11) or rules: a concept needs
// frontmatter, a non-empty type, a type rules knows (when it names any), and each of
// that type's required keys non-empty. index.md may carry frontmatter only at
// rules.RootIndex and only okf_version; index.md and log.md are never concepts.
// doc must not be nil.
func Conform(docRef *Doc, rules Rules) []Finding {
	doc := *docRef

	switch pathpkg.Base(doc.Path) {
	case IndexFile:
		return conformIndex(&doc, &rules)
	case LogFile:
		return conformLogFrontmatter(&doc)
	default:
		return conformConcept(&doc, &rules)
	}
}

// docFinding is a single finding on the document's first line.
func docFinding(doc *Doc, rule, msg string) []Finding {
	return []Finding{{Path: doc.Path, Line: 1, Rule: rule, Message: msg}}
}

// conformIndex checks an index.md: no frontmatter, except okf_version on the root.
func conformIndex(doc *Doc, rules *Rules) []Finding {
	switch {
	case !doc.HasFrontmatter:
		return nil
	case doc.Frontmatter.Type != "":
		return docFinding(
			doc,
			RuleReservedName,
			"index.md is a directory listing, not a concept: move the content to its own file",
		)
	case doc.Path == rules.RootIndex && onlyOKFVersion(&doc.Frontmatter):
		return nil
	default:
		return docFinding(
			doc,
			RuleIndexFrontmatter,
			"index.md takes no frontmatter, except okf_version on the bundle-root index",
		)
	}
}

// conformLogFrontmatter requires a log.md to have no frontmatter.
func conformLogFrontmatter(doc *Doc) []Finding {
	if !doc.HasFrontmatter {
		return nil
	}

	return docFinding(
		doc,
		RuleReservedName,
		"log.md is an update history, not a concept: remove its frontmatter",
	)
}

// conformConcept checks a concept's frontmatter against the type vocabulary.
func conformConcept(doc *Doc, rules *Rules) []Finding {
	frontmatter := doc.Frontmatter

	if !doc.HasFrontmatter {
		return docFinding(
			doc,
			RuleNoFrontmatter,
			"add a frontmatter block with at least `type`",
		)
	}

	if frontmatter.Type == "" {
		return docFinding(
			doc,
			RuleMissingKey,
			"frontmatter key `type` is missing or empty",
		)
	}

	required, known := rules.RequiredKeys[frontmatter.Type]
	if len(rules.RequiredKeys) > 0 && !known {
		return docFinding(
			doc,
			RuleUnknownType,
			"type `"+frontmatter.Type+"` is not in the vocabulary",
		)
	}

	var out []Finding

	for _, key := range required {
		if !HasKey(&frontmatter, key) {
			out = append(out, docFinding(
				doc,
				RuleMissingKey,
				"type `"+frontmatter.Type+"` requires frontmatter key `"+
					key+"`, which is missing or empty",
			)...)
		}
	}

	return out
}

// onlyOKFVersion reports whether fm carries okf_version and nothing else.
func onlyOKFVersion(fmRef *Frontmatter) bool {
	frontmatter := *fmRef

	for key := range stringKeys(&frontmatter) {
		if key != keyOKFVersion && HasKey(&frontmatter, key) {
			return false
		}
	}

	return frontmatter.OKFVersion != "" && len(frontmatter.Tags) == 0 &&
		frontmatter.Generated == nil &&
		len(frontmatter.Verified) == 0 &&
		len(frontmatter.Extra) == 0
}

// HasKey reports whether fm carries key with a non-empty value; for an extension key it
// looks in Extra.
// fm must not be nil.
func HasKey(fmRef *Frontmatter, key string) bool {
	frontmatter := *fmRef

	if field, ok := stringKeys(&frontmatter)[key]; ok {
		return *field != ""
	}

	switch key {
	case "tags":
		return len(frontmatter.Tags) > 0
	case "generated":
		return frontmatter.Generated != nil && frontmatter.Generated.By != ""
	case "verified":
		return len(frontmatter.Verified) > 0
	}

	v, ok := frontmatter.Extra[key]

	return ok && v != nil && v != ""
}

// ConformLog reports where a log.md breaks OKF §9 as this package reads it: every `##`
// heading outside a fenced block is a YYYY-MM-DD date, newest first with no repeats.
func ConformLog(path string, data []byte) []Finding {
	var (
		out    []Finding
		prev   time.Time
		fences fenceTracker
	)

	for index, line := range strings.Split(string(data), textNewline) {
		line = strings.TrimSuffix(line, textUd)
		if fences.consume(line) || fences.open != "" || !strings.HasPrefix(line, "## ") {
			continue
		}

		heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))

		var found []Finding

		prev, found = checkLogHeading(path, index+1, heading, prev)
		out = append(out, found...)
	}

	return out
}

// checkLogHeading validates one heading against the date above it, returning the
// date to compare the next heading with (unchanged when this one is not a date).
func checkLogHeading(
	path string,
	line int,
	heading string,
	prev time.Time,
) (time.Time, []Finding) {
	date, err := time.Parse(time.DateOnly, heading)
	if err != nil {
		return prev, []Finding{{
			Path: path, Line: line, Rule: RuleLogHeading,
			Message: "log heading `" + heading + "` is not a YYYY-MM-DD date",
		}}
	}

	if prev.IsZero() || date.Before(prev) {
		return date, nil
	}

	return date, []Finding{{
		Path: path,
		Line: line,
		Rule: RuleLogOrder,
		Message: "log date " + heading + " is not older than the heading above it: " +
			"keep dates newest first, one heading per date",
	}}
}

// fenceTracker follows code fences across a document's lines.
type fenceTracker struct {
	// open is the marker of the fence the current line is inside, "" when outside.
	open string
}

// consume updates the state for line, reporting whether line was a fence marker.
func (f *fenceTracker) consume(line string) bool {
	marker := fenceMarker(line)
	if marker == "" {
		return false
	}

	switch {
	case f.open == "":
		f.open = marker
	case strings.HasPrefix(marker, f.open[:1]):
		f.open = ""
	default:
		// No other value changes the outcome.
	}

	return true
}
