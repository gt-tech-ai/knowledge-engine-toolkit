package toolchain

import (
	"context"
	"fmt"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Detector checks tools for presence and minimum version. Existence and version
// lookups route through a CommandRunner so tests can inject a mock. Detection is
// platform-independent — only installation cares about the host platform.
type Detector struct {
	// runner performs the binary-existence and version-command lookups.
	runner interfaces.CommandRunner

	// log receives the per-tool status output.
	log interfaces.Logger
}

// NewDetector returns a Detector backed by the given runner and logger.
func NewDetector(runner interfaces.CommandRunner, log interfaces.Logger) *Detector {
	return &Detector{runner: runner, log: log}
}

// Check reports whether one tool is installed and meets its minimum version.
// A tool with no MinVersion (or no VersionCmd) is OK as long as its binary is on
// PATH. Version-check failures are reported as Outdated (found but unusable),
// while an unparseable version is treated as OK (best-effort, not a hard fail).
func (d *Detector) Check(
	ctx context.Context,
	root string,
	tool *types.ToolDef,
) types.ToolCheckResult {
	if !d.runner.Exists(tool.Binary) {
		d.log.Debug("tool lookup: not found", "tool", tool.Name, "binary", tool.Binary)
		return types.ToolCheckResult{
			Tool:    tool,
			Status:  types.StatusToolMissing,
			Message: tool.Name + ": not found",
		}
	}

	if tool.MinVersion == nil || len(tool.VersionCmd) == 0 {
		return types.ToolCheckResult{
			Tool:    tool,
			Status:  types.StatusToolOK,
			Found:   "installed",
			Message: tool.Name + ": OK (installed)",
		}
	}

	return d.checkVersion(ctx, root, tool)
}

// checkVersion runs the tool's version command and classifies the parsed result
// against its minimum version.
func (d *Detector) checkVersion(
	ctx context.Context,
	root string,
	tool *types.ToolDef,
) types.ToolCheckResult {
	d.log.Debug(
		"running version command",
		"tool",
		tool.Name,
		"cmd",
		strings.Join(tool.VersionCmd, " "),
	)
	result := d.runner.RunBuffered(ctx, root, tool.VersionCmd[0], tool.VersionCmd[1:]...)
	if result.Err != nil {
		d.log.Debug("version command failed", "tool", tool.Name, "error", result.Err)
		return types.ToolCheckResult{
			Tool:   tool,
			Status: types.StatusToolOutdated,
			Found:  "unknown",
			Message: fmt.Sprintf(
				"%s: found but version check failed: %v",
				tool.Name,
				result.Err,
			),
		}
	}

	raw := strings.TrimSpace(string(result.Stdout))
	d.log.Debug("raw version output", "tool", tool.Name, "output", raw)
	verStr := raw
	if tool.VersionParse != nil {
		verStr = tool.VersionParse(raw)
	}

	ver, err := types.ParseVersion(verStr)
	if err != nil {
		d.log.Debug("version parse error", "tool", tool.Name, "error", err)
		return types.ToolCheckResult{
			Tool:   tool,
			Status: types.StatusToolOK,
			Found:  verStr,
			Message: fmt.Sprintf(
				"%s: installed (version parse failed: %s)",
				tool.Name,
				verStr,
			),
		}
	}

	if !ver.MeetsMinimum(*tool.MinVersion) {
		return types.ToolCheckResult{
			Tool:   tool,
			Status: types.StatusToolOutdated,
			Found:  ver.String(),
			Message: fmt.Sprintf(
				"%s: %s found, need >= %s",
				tool.Name,
				ver,
				tool.MinVersion,
			),
		}
	}

	return types.ToolCheckResult{
		Tool:    tool,
		Status:  types.StatusToolOK,
		Found:   ver.String(),
		Message: fmt.Sprintf("%s: %s OK (installed)", tool.Name, ver),
	}
}

// Scan checks every tool, logs each result at its severity, and returns all
// results in order.
func (d *Detector) Scan(
	ctx context.Context,
	root string,
	tools []types.ToolDef,
) []types.ToolCheckResult {
	results := make([]types.ToolCheckResult, 0, len(tools))
	for i := range tools {
		result := d.Check(ctx, root, &tools[i])
		d.logResult(result)
		results = append(results, result)
	}
	return results
}

// CheckPrerequisites checks the prerequisite tools, logging each, and returns a
// consolidated CodeUnavailable error naming every missing or outdated one (with
// its fallback URL). Returns nil when all prerequisites are satisfied.
func (d *Detector) CheckPrerequisites(
	ctx context.Context,
	root string,
	prereqs []types.ToolDef,
) error {
	var missing []string
	for i := range prereqs {
		result := d.Check(ctx, root, &prereqs[i])
		switch result.Status {
		case types.StatusToolOK:
			d.log.Info(result.Message)
		case types.StatusToolOutdated:
			d.log.Error(result.Message)
			missing = append(missing, prereqHint(&prereqs[i], fmt.Sprintf(
				"need >= %s, found %s", prereqs[i].MinVersion, result.Found,
			)))
		case types.StatusToolMissing:
			d.log.Error(result.Message)
			missing = append(missing, prereqHint(&prereqs[i], "not found on PATH"))
		}
	}

	if len(missing) > 0 {
		return apperr.New(
			apperr.CodeUnavailable,
			"missing prerequisites:\n  "+strings.Join(missing, "\n  "),
		)
	}
	return nil
}

// ListOptional logs every optional tool with its install status and description,
// for a tool listing.
func (d *Detector) ListOptional(
	ctx context.Context,
	root string,
	optional []types.ToolDef,
) {
	d.log.Info("Optional developer tools")
	for i := range optional {
		t := &optional[i]
		status := "tool not installed"
		if d.Check(ctx, root, t).Status == types.StatusToolOK {
			status = "tool installed"
		}
		d.log.Info(status, "name", t.Name, "desc", t.Description)
	}
}

// logResult logs a tool check at the severity matching its status.
func (d *Detector) logResult(result types.ToolCheckResult) {
	switch result.Status {
	case types.StatusToolOK:
		d.log.Info(result.Message)
	case types.StatusToolOutdated:
		d.log.Warn(result.Message)
	case types.StatusToolMissing:
		d.log.Error(result.Message)
	}
}

// prereqHint formats "<Name>: <reason>" with the manual-install fallback URL
// appended when present.
func prereqHint(tool *types.ToolDef, reason string) string {
	if tool.FallbackURL != "" {
		reason += "\n  Install manually: " + tool.FallbackURL
	}
	return fmt.Sprintf("%s: %s", tool.Name, reason)
}

// SummarizeMissing consolidates a prerequisite error and a set of scan results
// into a single CodeUnavailable error listing every unmet dependency, or nil
// when nothing is missing.
func SummarizeMissing(prereqErr error, results []types.ToolCheckResult) error {
	var lines []string
	if prereqErr != nil {
		lines = append(lines, prereqErr.Error())
	}
	for i := range results {
		r := &results[i]
		if r.Status != types.StatusToolMissing &&
			r.Status != types.StatusToolOutdated {
			continue
		}
		msg := r.Tool.Name
		if r.Status == types.StatusToolOutdated && r.Found != "" {
			msg += fmt.Sprintf(" (found %s, need >= %s)", r.Found, r.Tool.MinVersion)
		}
		if r.Tool.FallbackURL != "" {
			msg += "\n    Install: " + r.Tool.FallbackURL
		}
		lines = append(lines, msg)
	}

	if len(lines) > 0 {
		return apperr.New(
			apperr.CodeUnavailable,
			"missing dependencies:\n  "+strings.Join(lines, "\n  "),
		)
	}
	return nil
}
