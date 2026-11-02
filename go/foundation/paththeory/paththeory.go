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

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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

// Compile validates and compiles rules over the built-in tokens plus the declared ones.
// An unknown token, a declared token with both or neither of a pattern and values, a
// rule with no kind or path, and a catch-all template (only `**` and `{any}` segments)
// are CodeInvalidInput.
func Compile(tokens map[string]Token, rules []Rule) (*Theory, error) {
	pats, err := tokenPatterns(tokens)
	if err != nil {
		return nil, err
	}
	th := &Theory{rules: make([]compiled, 0, len(rules))}
	for _, r := range rules {
		re, err := compileRule(r, pats)
		if err != nil {
			return nil, err
		}
		th.rules = append(th.rules, compiled{re: re, rule: r})
	}
	return th, nil
}

// tokenPatterns merges the built-ins with the declared tokens, each a regexp fragment.
func tokenPatterns(tokens map[string]Token) (map[string]string, error) {
	pats := Builtins()
	for name, t := range tokens {
		switch {
		case t.Pattern != "" && len(t.Values) > 0:
			return nil, apperr.New(apperr.CodeInvalidInput,
				"path theory: token {"+name+"} has both a pattern and values")
		case t.Pattern != "":
			if _, err := regexp.Compile(t.Pattern); err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
					"path theory: token {"+name+"} pattern")
			}
			pats[name] = t.Pattern
		case len(t.Values) > 0:
			quoted := make([]string, len(t.Values))
			for i, v := range t.Values {
				quoted[i] = regexp.QuoteMeta(v)
			}
			pats[name] = strings.Join(quoted, "|")
		default:
			return nil, apperr.New(apperr.CodeInvalidInput,
				"path theory: token {"+name+"} needs a pattern or values")
		}
	}
	return pats, nil
}

// compileRule turns a rule's template into an anchored regexp.
func compileRule(r Rule, pats map[string]string) (*regexp.Regexp, error) {
	if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Path) == "" {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"path theory: a rule needs a kind and a path ("+r.Kind+" "+r.Path+")")
	}
	segs := strings.Split(r.Path, "/")
	if !slices.ContainsFunc(
		segs,
		func(s string) bool { return s != "**" && s != "{any}" },
	) {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"path theory: "+r.Path+" ("+r.Kind+") is a catch-all")
	}
	var b strings.Builder
	b.WriteString("^")
	for i, seg := range segs {
		last := i == len(segs)-1
		if seg == "**" {
			if last {
				b.WriteString(`[^/]+(?:/[^/]+)*`)
			} else {
				b.WriteString(`(?:[^/]+/)*`)
			}
			continue
		}
		frag, err := segmentPattern(seg, pats, r)
		if err != nil {
			return nil, err
		}
		b.WriteString(frag)
		if !last {
			b.WriteString("/")
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "path theory: "+r.Path)
	}
	return re, nil
}

// segmentPattern turns one template segment (literal text and tokens) into a fragment.
func segmentPattern(seg string, pats map[string]string, r Rule) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range tokenRE.FindAllStringSubmatchIndex(seg, -1) {
		b.WriteString(regexp.QuoteMeta(seg[last:m[0]]))
		name := seg[m[2]:m[3]]
		p, ok := pats[name]
		if !ok {
			return "", apperr.New(apperr.CodeInvalidInput,
				"path theory: "+r.Path+" ("+r.Kind+") uses unknown token {"+name+"}")
		}
		b.WriteString("(?:" + p + ")")
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(seg[last:]))
	return b.String(), nil
}

// Claim returns the first rule whose template matches path; a forbid rule is returned
// too, so the caller checks Forbid.
func (t *Theory) Claim(path string) (Rule, bool) {
	for _, c := range t.rules {
		if c.re.MatchString(path) {
			return c.rule, true
		}
	}
	return Rule{}, false
}

// Rules returns the theory's rules in claim order.
func (t *Theory) Rules() []Rule {
	out := make([]Rule, len(t.rules))
	for i, c := range t.rules {
		out[i] = c.rule
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
	for p, r := range c.Claims {
		out[p] = r.Kind
	}
	return out
}

// Classify claims every path and reports the forbidden and unclaimed paths and the dead
// rules.
func (t *Theory) Classify(paths []string) Classification {
	c := Classification{Claims: map[string]Rule{}, Forbidden: map[string]Rule{}}
	used := make([]bool, len(t.rules))
	for _, p := range paths {
		claimed := false
		for i, r := range t.rules {
			if r.re.MatchString(p) {
				if r.rule.Forbid {
					c.Forbidden[p] = r.rule
				} else {
					c.Claims[p] = r.rule
				}
				used[i], claimed = true, true
				break
			}
		}
		if !claimed {
			c.Unclaimed = append(c.Unclaimed, p)
		}
	}
	for i, r := range t.rules {
		if !used[i] && !r.rule.Forbid {
			c.Dead = append(c.Dead, r.rule)
		}
	}
	return c
}
