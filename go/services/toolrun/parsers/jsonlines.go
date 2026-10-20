package parsers

import (
	"encoding/json"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// jsonLines is KindJSONLines.
type jsonLines struct{}

// Parse reads one finding per stdout line holding a JSON object with `message` and
// optional `file`, `line` and `rule`; other lines are ignored.
func (jsonLines) Parse(res interfaces.CmdResult) types.Verdict {
	var findings []types.Finding
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		var f types.Finding
		if json.Unmarshal([]byte(line), &f) != nil || f.Message == "" {
			continue
		}
		findings = append(findings, f)
	}
	return verdict(res, findings)
}
