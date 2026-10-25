package ghactions

import (
	"fmt"
	"strings"
)

// codeFence opens and closes a markdown code block.
const codeFence = "```"

// TruncateComment cuts s to at most limit characters (runes, GitHub's unit) for an
// issue or pull-request comment, keeping its head and its tail around a notice naming
// how many characters were removed. A cut inside a code fence or a <details> block
// closes it before the notice and reopens it after, so the notice renders as text and
// the blocks stay balanced on both sides. A body within the limit is unchanged; a limit
// too small for the notice keeps only the first limit characters.
func TruncateComment(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	keep := limit - len([]rune(truncationNotice(len(runes))))
	if keep <= 0 {
		return string(runes[:limit])
	}
	headLen := (keep + 1) / 2
	tailLen := keep - headLen
	cut := len(runes) - keep
	head, tail := string(runes[:headLen]), string(runes[len(runes)-tailLen:])
	closing, opening := balanceCut(head, tail)
	if closing != "" || opening != "" {
		extra := len([]rune(closing)) + len([]rune(opening))
		if keep -= extra; keep <= 0 {
			return string(runes[:limit])
		}
		headLen = (keep + 1) / 2
		tailLen = keep - headLen
		cut = len(runes) - keep
		head, tail = string(runes[:headLen]), string(runes[len(runes)-tailLen:])
	}
	return head + closing + truncationNotice(cut) + opening + tail
}

// balanceCut returns what closes the markdown blocks head leaves open (a code fence,
// <details> elements) and what reopens the blocks tail closes without opening, so the
// notice between them sits outside every block.
func balanceCut(head, tail string) (closing, opening string) {
	var c, o strings.Builder
	if strings.Count(head, codeFence)%2 == 1 {
		c.WriteString("\n" + codeFence)
	}
	for range strings.Count(head, "<details>") - strings.Count(head, "</details>") {
		c.WriteString("\n</details>")
	}
	for range strings.Count(tail, "</details>") - strings.Count(tail, "<details>") {
		o.WriteString("<details>\n")
	}
	if strings.Count(tail, codeFence)%2 == 1 {
		o.WriteString(codeFence + "\n")
	}
	return c.String(), o.String()
}

// truncationNotice is the line the cut leaves between the head and the tail.
func truncationNotice(cut int) string {
	return fmt.Sprintf("\n\n… %d characters cut to fit GitHub's comment limit: the full "+
		"summary is in the workflow run's job summary …\n\n", cut)
}
