package parsers

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// gccRe matches `file:line[:col]: message`.
var gccRe = regexp.MustCompile(`^([^:\s][^:]*):(\d+):(?:\d+:)?\s*(.+)$`)

// gcc is KindGCC.
type gcc struct{}

// Parse reads one finding per `file:line[:col]: message` line of stdout and stderr.
// res must not be nil.
func (gcc) Parse(res *interfaces.CmdResult) types.Verdict {
	var findings []types.Finding

	for _, out := range [][]byte{res.Stdout, res.Stderr} {
		for line := range strings.SplitSeq(string(out), "\n") {
			items := gccRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
			if items == nil {
				continue
			}

			n, _ := strconv.Atoi(items[2])

			findings = append(
				findings,
				types.Finding{File: items[1], Line: n, Message: items[3]},
			)
		}
	}

	return verdict(res, findings)
}
