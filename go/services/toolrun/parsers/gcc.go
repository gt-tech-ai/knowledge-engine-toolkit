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
func (gcc) Parse(res interfaces.CmdResult) types.Verdict {
	var findings []types.Finding
	for _, out := range [][]byte{res.Stdout, res.Stderr} {
		for line := range strings.SplitSeq(string(out), "\n") {
			m := gccRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			findings = append(findings, types.Finding{File: m[1], Line: n, Message: m[3]})
		}
	}
	return verdict(res, findings)
}
