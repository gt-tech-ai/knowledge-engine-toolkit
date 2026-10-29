// Package imagescan is a container-image vulnerability gate: a Trivy image scan under a
// TrivyPolicy (ScanImage), and the known-exploited verdict over a grype JSON report
// (GrypeEmergencies, CheckGrypeReport), which blocks only on a vulnerability that is
// known to be exploited (CISA KEV) and that an upgrade fixes.
package imagescan

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// trivyStep is the result row ScanImage reports.
const trivyStep = "trivy image"

// TrivyPolicy is what a Trivy image scan runs under.
type TrivyPolicy struct {
	// Scanners is the comma-separated Trivy scanner list (e.g. "vuln").
	Scanners string

	// Severity is the comma-separated Trivy severity list (e.g. "HIGH,CRITICAL").
	Severity string

	// IgnoreFile is the Trivy ignore file, relative to the scan's directory: the baseline
	// of accepted findings.
	IgnoreFile string

	// IgnoreUnfixed limits the scan to findings that have a fixed version.
	IgnoreUnfixed bool

	// ReportOnly makes Trivy report without failing (--exit-code 0), for a gate that
	// another verdict decides (the grype known-exploited report).
	ReportOnly bool
}

// Validate reports a policy missing its scanners, severities or ignore file as
// CodeInvalidInput.
func (p TrivyPolicy) Validate() error {
	if p.Scanners == "" || p.Severity == "" || p.IgnoreFile == "" {
		return apperr.New(apperr.CodeInvalidInput,
			"image scan policy: scanners, severity and ignore file are required")
	}
	return nil
}

// Args is the `trivy` argv that scans ref under the policy.
func (p TrivyPolicy) Args(ref string) []string {
	args := []string{"image", "--scanners", p.Scanners, "--severity", p.Severity}
	if p.IgnoreUnfixed {
		args = append(args, "--ignore-unfixed")
	}
	exit := "1"
	if p.ReportOnly {
		exit = "0"
	}
	return append(args, "--ignorefile", p.IgnoreFile, "--exit-code", exit, ref)
}

// ScanImage scans the container image ref (a local tag or a pushed `<repo>@sha256:…`
// digest) with Trivy under policy, run through runner in dir. A remaining finding fails
// the row, unless the policy is ReportOnly. A missing trivy is a Skip, or a Fail
// (CodeNotFound) under strict, so a broken install can't silently pass a gate; an
// invalid policy is a Fail.
func ScanImage(
	ctx context.Context,
	runner interfaces.CommandRunner,
	dir string,
	policy TrivyPolicy,
	ref string,
	strict bool,
) types.StepResults {
	if !runner.Exists("trivy") {
		if !strict {
			return types.OneStep(trivyStep, types.StatusSkip, nil)
		}
		return types.OneStep(trivyStep, types.StatusFail, apperr.New(
			apperr.CodeNotFound, "trivy not found on PATH; a strict scan can't run"))
	}
	if err := policy.Validate(); err != nil {
		return types.OneStep(trivyStep, types.StatusFail, err)
	}
	if err := runner.Run(ctx, dir, "trivy", policy.Args(ref)...); err != nil {
		return types.OneStep(trivyStep, types.StatusFail,
			apperr.Wrap(err, apperr.CodeInternal, "unignored vulnerabilities in "+ref))
	}
	return types.OneStep(trivyStep, types.StatusPass, nil)
}
