// Package paththeory classifies repository paths against an ordered set of path rules:
// each rule names a kind of file and the template its whole path follows, folders and
// file name at once. A path is claimed by the first rule whose template matches it, so a
// theory says where every kind of file lives and what it is called, and a path no rule
// claims is drift.
package paththeory

import (
	"regexp"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// Rule is one kind of file and the path template it follows.
type Rule struct {
	// Kind names what the file is ("Go unit test", "ADR").
	Kind string `yaml:"kind" json:"kind"`

	// Path is the template over the whole repository-relative path: literal text, `**`
	// for any number of folders (none included) and `{token}` for one token's match
	// within a folder or file name.
	Path string `yaml:"path" json:"path"`

	// Generator names the generator that owns files of this kind; empty when they are
	// hand-written.
	Generator string `yaml:"generator,omitempty" json:"generator,omitempty"`

	// Forbid makes the rule name a violation: a path it claims first is Forbidden under
	// its kind, so a misplaced file is reported by name instead of falling to a looser
	// rule. A forbid rule that matches nothing is not dead.
	Forbid bool `yaml:"forbid,omitempty" json:"forbid,omitempty"`
}

// Token is a declared template token: a pattern or a closed list of values, never both.
type Token struct {
	// Pattern is the regular expression the token matches within one path segment.
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`

	// Values is the closed list of literal values the token matches.
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
}

// Builtins returns the tokens every theory has: the name styles and number shapes.
func Builtins() map[string]string {
	return map[string]string{
		"slug":    `[a-z][a-z0-9]*(?:-[a-z0-9]+)*`,
		"snake":   `[a-z][a-z0-9]*(?:_[a-z0-9]+)*`,
		"pascal":  `[A-Z][A-Za-z0-9]*`,
		"camel":   `[a-z][a-z0-9]*(?:[A-Z][a-z0-9]*)*`,
		"segment": `[a-z0-9]+(?:[.-][a-z0-9]+)*`,
		"date":    `\d{4}-\d{2}-\d{2}`,
		"nnnn":    `\d{4}`,
		"nn":      `\d{2,}`,
		"n":       `\d+`,
		"any":     `[^/]+`,
	}
}

// compiled is one rule with its anchored matcher.
type compiled struct {
	// re matches the rule's whole template.
	re *regexp.Regexp

	// rule is the source rule.
	rule Rule
}

// Theory is a compiled, ordered rule set.
type Theory struct {
	// rules are the compiled rules in claim order.
	rules []compiled
}

// tokenRE finds the `{name}` tokens in a template segment.
var tokenRE = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// Submatch index positions in a tokenRE match: the whole match spans [matchStart,
// matchEnd) and the token's name [nameStart, nameEnd).
const (
	// matchStart is the index of the whole match's start offset.
	matchStart = 0

	// matchEnd is the index of the whole match's end offset.
	matchEnd = 1

	// nameStart is the index of the token name's start offset.
	nameStart = 2

	// nameEnd is the index of the token name's end offset.
	nameEnd = 3
)

// Regexp fragments for the `**` template segment.
const (
	// anyFoldersLast is `**` as the last segment: one or more path segments.
	anyFoldersLast = `[^/]+(?:/[^/]+)*`

	// anyFolders is `**` before another segment: zero or more folders.
	anyFolders = `(?:[^/]+/)*`

	// folderMark stands in for a `**` segment while the fragments are joined; a
	// QuoteMeta'd literal never contains it.
	folderMark = "\x00"
)

// Compile validates and compiles rules over the built-in tokens plus the declared ones.
// An unknown token, a declared token with both or neither of a pattern and values, a
// rule with no kind or path, and a catch-all template (only `**` and `{any}` segments)
// are CodeInvalidInput.
func Compile(tokens map[string]Token, rules []Rule) (*Theory, error) {
	pats, err := tokenPatterns(tokens)
	if err != nil {
		return nil, err
	}

	theory := &Theory{rules: make([]compiled, 0, len(rules))}
	for _, rule := range rules {
		re, err := compileRule(&rule, pats)
		if err != nil {
			return nil, err
		}

		theory.rules = append(theory.rules, compiled{re: re, rule: rule})
	}

	return theory, nil
}

// tokenPatterns merges the built-ins with the declared tokens, each a regexp fragment.
func tokenPatterns(tokens map[string]Token) (map[string]string, error) {
	pats := Builtins()

	for name, token := range tokens {
		pattern, err := tokenPattern(name, token)
		if err != nil {
			return nil, err
		}

		pats[name] = pattern
	}

	return pats, nil
}

// tokenPattern is the regexp fragment of the declared token name: its pattern, or its
// values as quoted alternatives.
func tokenPattern(name string, token Token) (string, error) {
	switch {
	case token.Pattern != "" && len(token.Values) > 0:
		return "", apperr.New(apperr.CodeInvalidInput,
			"path theory: token {"+name+"} has both a pattern and values")
	case token.Pattern != "":
		_, err := regexp.Compile(token.Pattern)
		if err != nil {
			return "", apperr.Wrap(err, apperr.CodeInvalidInput,
				"path theory: token {"+name+"} pattern")
		}

		return token.Pattern, nil
	case len(token.Values) > 0:
		quoted := make([]string, len(token.Values))
		for index, value := range token.Values {
			quoted[index] = regexp.QuoteMeta(value)
		}

		return strings.Join(quoted, "|"), nil
	default:
		return "", apperr.New(apperr.CodeInvalidInput,
			"path theory: token {"+name+"} needs a pattern or values")
	}
}

// compileRule turns a rule's template into an anchored regexp.
func compileRule(rule *Rule, pats map[string]string) (*regexp.Regexp, error) {
	segs, err := templateSegments(rule)
	if err != nil {
		return nil, err
	}

	frags := make([]string, len(segs))
	for index, seg := range segs {
		if seg == "**" {
			frags[index] = folderMark

			continue
		}

		frags[index], err = segmentPattern(seg, pats, rule)
		if err != nil {
			return nil, err
		}
	}

	// A `**` before another segment absorbs the separator after it, so it may match no
	// folder at all; a trailing `**` matches one or more segments.
	joined := strings.NewReplacer(folderMark+"/", anyFolders, folderMark, anyFoldersLast).
		Replace(strings.Join(frags, "/"))

	re, err := regexp.Compile("^" + joined + "$")
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "path theory: "+rule.Path)
	}

	return re, nil
}

// templateSegments validates rule (a kind, a path, not a catch-all) and splits its
// template into folder segments.
func templateSegments(rule *Rule) ([]string, error) {
	if strings.TrimSpace(rule.Kind) == "" || strings.TrimSpace(rule.Path) == "" {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"path theory: a rule needs a kind and a path ("+rule.Kind+" "+rule.Path+")")
	}

	segs := strings.Split(rule.Path, "/")
	if !slices.ContainsFunc(
		segs,
		func(s string) bool { return s != "**" && s != "{any}" },
	) {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"path theory: "+rule.Path+" ("+rule.Kind+") is a catch-all")
	}

	return segs, nil
}

// segmentPattern turns one template segment (literal text and tokens) into a fragment.
func segmentPattern(seg string, pats map[string]string, rule *Rule) (string, error) {
	var frag strings.Builder

	last := 0

	for _, match := range tokenRE.FindAllStringSubmatchIndex(seg, -1) {
		_, _ = frag.WriteString(regexp.QuoteMeta(seg[last:match[matchStart]]))

		name := seg[match[nameStart]:match[nameEnd]]

		pattern, known := pats[name]
		if !known {
			return "", apperr.New(
				apperr.CodeInvalidInput,
				"path theory: "+rule.Path+" ("+rule.Kind+") uses unknown token {"+name+"}",
			)
		}

		_, _ = frag.WriteString("(?:" + pattern + ")")

		last = match[matchEnd]
	}

	_, _ = frag.WriteString(regexp.QuoteMeta(seg[last:]))

	return frag.String(), nil
}

// Claim returns the first rule whose template matches path; a forbid rule is returned
// too, so the caller checks Forbid.
func (t *Theory) Claim(path string) (Rule, bool) {
	index := t.claimIndex(path)
	if index < 0 {
		return stdx.Zero[Rule](), false
	}

	return t.rules[index].rule, true
}

// Rules returns the theory's rules in claim order.
func (t *Theory) Rules() []Rule {
	out := make([]Rule, len(t.rules))
	for index := range t.rules {
		out[index] = t.rules[index].rule
	}

	return out
}

// Classification is one pass of a theory over a tree.
type Classification struct {
	// Claims maps each claimed path to the rule that claims it.
	Claims map[string]Rule

	// Forbidden maps each path a forbid rule claimed first to that rule.
	Forbidden map[string]Rule

	// Unclaimed are the paths no rule claims, in input order.
	Unclaimed []string

	// Dead are the rules that claimed no path (unused or shadowed), in claim order.
	Dead []Rule
}

// Kinds maps each claimed path to its rule's kind.
func (c Classification) Kinds() map[string]string {
	out := make(map[string]string, len(c.Claims))
	for path, rule := range c.Claims {
		out[path] = rule.Kind
	}

	return out
}

// Classify claims every path and reports the forbidden and unclaimed paths and the dead
// rules.
func (t *Theory) Classify(paths []string) Classification {
	result := Classification{
		Claims:    map[string]Rule{},
		Forbidden: map[string]Rule{},
		Unclaimed: nil,
		Dead:      nil,
	}
	used := make([]bool, len(t.rules))

	for _, path := range paths {
		index := t.claimIndex(path)
		if index < 0 {
			result.Unclaimed = append(result.Unclaimed, path)

			continue
		}

		used[index] = true

		record(&result, path, t.rules[index].rule)
	}

	result.Dead = t.dead(used)

	return result
}

// claimIndex is the index of the first rule whose template matches path; -1 when none
// does.
func (t *Theory) claimIndex(path string) int {
	for index := range t.rules {
		if t.rules[index].re.MatchString(path) {
			return index
		}
	}

	return -1
}

// dead returns the rules, in claim order, that used marks as claiming nothing; a forbid
// rule is never dead.
func (t *Theory) dead(used []bool) []Rule {
	var out []Rule

	for index := range t.rules {
		if !used[index] && !t.rules[index].rule.Forbid {
			out = append(out, t.rules[index].rule)
		}
	}

	return out
}

// record files path in result under rule: Forbidden for a forbid rule, Claims otherwise.
func record(result *Classification, path string, rule Rule) {
	if rule.Forbid {
		result.Forbidden[path] = rule

		return
	}

	result.Claims[path] = rule
}
