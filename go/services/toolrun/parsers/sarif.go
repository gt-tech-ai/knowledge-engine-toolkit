package parsers

import (
	"encoding/json"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// sarifLog is the part of a SARIF 2.1 log the parser reads.
type sarifLog struct {
	// Runs are the log's tool runs.
	Runs []struct {
		// Results are the run's reported problems.
		Results []struct {
			// RuleID names the violated rule.
			RuleID string `json:"ruleId"`

			// Message holds the problem's text.
			Message struct {
				// Text is the message.
				Text string `json:"text"`
			} `json:"message"`

			// Locations are where the problem is; the first is used.
			Locations []struct {
				// PhysicalLocation is the file and region.
				PhysicalLocation struct {
					// ArtifactLocation names the file.
					ArtifactLocation struct {
						// URI is the file's path.
						URI string `json:"uri"`
					} `json:"artifactLocation"`

					// Region is the span in the file.
					Region struct {
						// StartLine is the 1-based first line.
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

// sarif is KindSARIF.
type sarif struct{}

// Parse reads stdout as a SARIF log, one finding per result. Output that is not SARIF
// yields no findings, so the exit code alone decides.
func (sarif) Parse(res interfaces.CmdResult) types.Verdict {
	var log sarifLog
	if json.Unmarshal(res.Stdout, &log) != nil {
		return verdict(res, nil)
	}
	var findings []types.Finding
	for _, run := range log.Runs {
		for _, r := range run.Results {
			f := types.Finding{Rule: r.RuleID, Message: r.Message.Text}
			if len(r.Locations) > 0 {
				loc := r.Locations[0].PhysicalLocation
				f.File, f.Line = loc.ArtifactLocation.URI, loc.Region.StartLine
			}
			findings = append(findings, f)
		}
	}
	return verdict(res, findings)
}
