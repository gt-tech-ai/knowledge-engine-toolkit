package parsers

import (
	"encoding/json"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// sarifLogRegion is the Region element of sarifLog.
type sarifLogRegion struct {
	// StartLine is the 1-based first line.
	StartLine int `json:"startLine"`
}

// sarifLogMessage is the Message element of sarifLog.
type sarifLogMessage struct {
	// Text is the message.
	Text string `json:"text"`
}

// sarifLogArtifactLocation is the ArtifactLocation element of sarifLog.
type sarifLogArtifactLocation struct {
	// URI is the file's path.
	URI string `json:"uri"`
}

// sarifLogPhysicalLocation is the PhysicalLocation element of sarifLog.
type sarifLogPhysicalLocation struct {
	// ArtifactLocation names the file.
	ArtifactLocation sarifLogArtifactLocation `json:"artifactLocation"`

	// Region is the span in the file.
	Region sarifLogRegion `json:"region"`
}

// sarifLogLocations is the Locations element of sarifLog.
type sarifLogLocations struct {
	// PhysicalLocation is the file and region.
	PhysicalLocation sarifLogPhysicalLocation `json:"physicalLocation"`
}

// sarifLogResult is one problem a run reported.
type sarifLogResult struct {
	// RuleID names the violated rule.
	RuleID string `json:"ruleId"`

	// Message holds the problem's text.
	Message sarifLogMessage `json:"message"`

	// Locations are where the problem is; the first is used.
	Locations []sarifLogLocations `json:"locations"`
}

// sarifLogRun is one tool run in the log.
type sarifLogRun struct {
	// Results are the run's reported problems.
	Results []sarifLogResult `json:"results"`
}

// sarifLog is the part of a SARIF 2.1 log the parser reads.
type sarifLog struct {
	// Runs are the log's tool runs.
	Runs []sarifLogRun `json:"runs"`
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
