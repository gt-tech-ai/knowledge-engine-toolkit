// Package imagescan is a container-image vulnerability gate: a Trivy image scan under a
// TrivyPolicy (ScanImage), and the known-exploited verdict over a grype JSON report
// (GrypeEmergencies, CheckGrypeReport), which blocks only on a vulnerability that is
// known to be exploited (CISA KEV) and that an upgrade fixes.
package imagescan

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

// trivyBinary is the scanner's executable.
const trivyBinary = "trivy"

// ScanImage scans the container image ref (a local tag or a pushed `<repo>@sha256:…`
// digest) with Trivy under policy, run through runner in dir. A remaining finding fails
// the row, unless the policy is ReportOnly. A missing trivy is a Skip; an invalid policy
// is a Fail.
func ScanImage(
	ctx context.Context,
	runner interfaces.CommandRunner,
	dir string,
	policy TrivyPolicy,
	ref string,
) types.StepResults {
	if !runner.Exists(trivyBinary) {
		return types.OneStep(trivyStep, types.StatusSkip, nil)
	}

	return runTrivy(ctx, runner, dir, policy, ref)
}

// ScanImageStrict is ScanImage, except that a missing trivy is a Fail (CodeNotFound),
// so a broken install can't silently pass a gate.
func ScanImageStrict(
	ctx context.Context,
	runner interfaces.CommandRunner,
	dir string,
	policy TrivyPolicy,
	ref string,
) types.StepResults {
	if !runner.Exists(trivyBinary) {
		return types.OneStep(trivyStep, types.StatusFail, apperr.New(
			apperr.CodeNotFound,
			"trivy not found on PATH; a strict scan can't run",
		))
	}

	return runTrivy(ctx, runner, dir, policy, ref)
}

// runTrivy validates policy and scans ref with an installed trivy.
func runTrivy(
	ctx context.Context,
	runner interfaces.CommandRunner,
	dir string,
	policy TrivyPolicy,
	ref string,
) types.StepResults {
	err := policy.Validate()
	if err != nil {
		return types.OneStep(trivyStep, types.StatusFail, err)
	}

	err = runner.Run(ctx, dir, trivyBinary, policy.Args(ref)...)
	if err != nil {
		return types.OneStep(trivyStep, types.StatusFail,
			apperr.Wrap(err, apperr.CodeInternal, "unignored vulnerabilities in "+ref))
	}

	return types.OneStep(trivyStep, types.StatusPass, nil)
}
