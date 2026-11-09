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
func Conform(doc Doc, rules Rules) []Finding {
	finding := func(rule, msg string) []Finding {
		return []Finding{{Path: doc.Path, Line: 1, Rule: rule, Message: msg}}
	}
	fm := doc.Frontmatter

	switch pathpkg.Base(doc.Path) {
	case IndexFile:
		switch {
		case !doc.HasFrontmatter:
			return nil
		case fm.Type != "":
			return finding(
				RuleReservedName,
				"index.md is a directory listing, not a concept: move the content to its own file",
			)
		case doc.Path == rules.RootIndex && onlyOKFVersion(fm):
			return nil
		default:
			return finding(
				RuleIndexFrontmatter,
				"index.md takes no frontmatter, except okf_version on the bundle-root index",
			)
		}
	case LogFile:
		if doc.HasFrontmatter {
			return finding(
				RuleReservedName,
				"log.md is an update history, not a concept: remove its frontmatter",
			)
		}

		return nil
	}

	if !doc.HasFrontmatter {
		return finding(RuleNoFrontmatter, "add a frontmatter block with at least `type`")
	}

	if fm.Type == "" {
		return finding(RuleMissingKey, "frontmatter key `type` is missing or empty")
	}

	required, known := rules.RequiredKeys[fm.Type]
	if len(rules.RequiredKeys) > 0 && !known {
		return finding(RuleUnknownType, "type `"+fm.Type+"` is not in the vocabulary")
	}

	var out []Finding

	for _, key := range required {
		if !HasKey(fm, key) {
			out = append(out, finding(
				RuleMissingKey,
				"type `"+fm.Type+"` requires frontmatter key `"+key+"`, which is missing or empty",
			)...)
		}
	}

	return out
}

// onlyOKFVersion reports whether fm carries okf_version and nothing else.
func onlyOKFVersion(fm Frontmatter) bool {
	for key := range stringKeys(&fm) {
		if key != keyOKFVersion && HasKey(fm, key) {
			return false
		}
	}

	return fm.OKFVersion != "" && len(fm.Tags) == 0 && fm.Generated == nil &&
		len(fm.Verified) == 0 && len(fm.Extra) == 0
}

// HasKey reports whether fm carries key with a non-empty value; for an extension key it
// looks in Extra.
func HasKey(fm Frontmatter, key string) bool {
	if field, ok := stringKeys(&fm)[key]; ok {
		return *field != ""
	}

	switch key {
	case "tags":
		return len(fm.Tags) > 0
	case "generated":
		return fm.Generated != nil && fm.Generated.By != ""
	case "verified":
		return len(fm.Verified) > 0
	}

	v, ok := fm.Extra[key]

	return ok && v != nil && v != ""
}

// ConformLog reports where a log.md breaks OKF §9 as this package reads it: every `##`
// heading outside a fenced block is a YYYY-MM-DD date, newest first with no repeats.
func ConformLog(path string, data []byte) []Finding {
	var (
		out  []Finding
		prev time.Time
		open string
	)

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if marker := fenceMarker(line); marker != "" {
			if open == "" {
				open = marker
			} else if strings.HasPrefix(marker, open[:1]) {
				open = ""
			}

			continue
		}

		if open != "" || !strings.HasPrefix(line, "## ") {
			continue
		}

		heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))

		date, err := time.Parse(time.DateOnly, heading)
		if err != nil {
			out = append(out, Finding{
				Path: path, Line: i + 1, Rule: RuleLogHeading,
				Message: "log heading `" + heading + "` is not a YYYY-MM-DD date",
			})

			continue
		}

		if !prev.IsZero() && !date.Before(prev) {
			out = append(out, Finding{
				Path: path,
				Line: i + 1,
				Rule: RuleLogOrder,
				Message: "log date " + heading + " is not older than the heading above it: " +
					"keep dates newest first, one heading per date",
			})
		}

		prev = date
	}

	return out
}
