package okf

// Attribution records who or what acted on a concept and when (OKF §5.2).
type Attribution struct {
	// By is the actor: `<producer>/<version>` for an agent or tool, `human:<id>` for a
	// person, `process:<id>` for an automated process (OKF §7).
	By string `yaml:"by"`

	// At is an ISO 8601 datetime with an explicit UTC offset; empty when unknown.
	At string `yaml:"at,omitempty"`
}

// Frontmatter is a concept's YAML metadata block. The named fields are the OKF keys a
// bundle tool reads; every other key is kept in Extra.
type Frontmatter struct {
	// Type identifies the kind of concept; the only key OKF always requires.
	Type string

	// Title is the human-readable display name.
	Title string

	// Description is a one-sentence summary, used by index.md entries.
	Description string

	// Resource is the canonical URI of the asset the concept describes.
	Resource string

	// Status is the lifecycle state: draft, stable or deprecated; empty means stable.
	Status string

	// StaleAfter is the ISO 8601 instant from which the content is stale; empty when
	// the concept never goes stale.
	StaleAfter string

	// OKFVersion is the format version a bundle-root index.md declares.
	OKFVersion string

	// Generated records how the current content was produced; nil when not recorded.
	Generated *Attribution

	// Extra holds every key not named above, by key, as decoded YAML values.
	Extra map[string]any

	// Tags are short strings for cross-cutting categorization.
	Tags []string

	// Verified lists the confirmations of the content. A bare mapping in the source
	// parses as a one-element list (OKF §5.2).
	Verified []Attribution
}

// LinkKind classifies a link target.
type LinkKind int

const (
	// LinkRelative is a path relative to the linking file, e.g. `../guides/x.md`.
	LinkRelative LinkKind = iota

	// LinkAbsolute is a bundle-relative path beginning with `/`.
	LinkAbsolute

	// LinkAnchor is a fragment within the same file, e.g. `#usage`.
	LinkAnchor

	// LinkExternal has a URL scheme (`https:`, `mailto:`) or starts with `//`.
	LinkExternal
)

// Link is one markdown link in a body: an inline link or image, or a reference
// definition. Links inside fenced code blocks and inline code spans are not links.
type Link struct {
	// Target is the destination without its fragment; empty for an anchor link.
	Target string

	// Fragment is the part after `#`, without the `#`; empty when there is none.
	Fragment string

	// Line is the 1-based line of the link in the whole file, frontmatter included.
	Line int

	// Kind classifies Target.
	Kind LinkKind
}

// Doc is one parsed markdown file.
type Doc struct {
	// Frontmatter is the parsed block; zero when HasFrontmatter is false.
	Frontmatter Frontmatter

	// Path is the file's path as the caller named it; findings report it.
	Path string

	// Body is everything after the frontmatter block (the whole file without one).
	Body string

	// Links are the body's links in document order.
	Links []Link

	// BodyStartLine is the 1-based file line the body starts on.
	BodyStartLine int

	// HasFrontmatter reports whether the file opens with a `---` frontmatter block.
	HasFrontmatter bool
}

// Rule names reported in Finding.Rule.
const (
	// RuleNoFrontmatter: a concept has no frontmatter block.
	RuleNoFrontmatter = "no-frontmatter"

	// RuleUnknownType: the type is not one of Rules.RequiredKeys' keys.
	RuleUnknownType = "unknown-type"

	// RuleMissingKey: a required key is absent or empty.
	RuleMissingKey = "missing-key"

	// RuleReservedName: index.md or log.md is used as a concept.
	RuleReservedName = "reserved-name"

	// RuleIndexFrontmatter: an index.md carries frontmatter it may not.
	RuleIndexFrontmatter = "index-frontmatter"

	// RuleLogHeading: a log.md `##` heading is not a YYYY-MM-DD date.
	RuleLogHeading = "log-heading"

	// RuleLogOrder: log.md dates are not strictly newest first.
	RuleLogOrder = "log-order"
)
