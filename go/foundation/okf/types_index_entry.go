package okf

// IndexEntry is one line of an index.md listing.
type IndexEntry struct {
	// Title is the link text: the concept's title, or a subdirectory's name.
	Title string

	// Link is the entry's target, relative to the index.md (a subdirectory ends in `/`).
	Link string

	// Description follows the link after ` - `; omitted when empty.
	Description string

	// Section is the heading the entry is grouped under; entries with no section are
	// listed first, before any heading.
	Section string
}
