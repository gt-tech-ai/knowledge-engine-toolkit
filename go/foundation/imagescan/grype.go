package imagescan

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Emergency is one image-scan finding the gate blocks a deploy on: a vulnerability that
// is known to be exploited (it is on CISA's KEV catalogue) and that an upgrade fixes.
type Emergency struct {
	// ID is the advisory id (e.g. CVE-2026-0001).
	ID string

	// Severity is grype's severity for the advisory (e.g. "High").
	Severity string

	// Package is the affected package's name.
	Package string

	// Version is the installed version of Package.
	Version string

	// FixedIn is the first version that fixes the advisory.
	FixedIn string
}

// grypeReport is the slice of grype's `-o json` report the emergency policy reads.
type grypeReport struct {
	// Matches are grype's vulnerability matches, one per (advisory, package).
	Matches []struct {
		// Artifact is the matched package.
		Artifact struct {
			// Name is the package name.
			Name string `json:"name"`

			// Version is the installed version.
			Version string `json:"version"`
		} `json:"artifact"`

		// Vulnerability is the matched advisory.
		Vulnerability struct {
			// ID is the advisory id.
			ID string `json:"id"`

			// Severity is grype's severity label.
			Severity string `json:"severity"`

			// KnownExploited is the advisory's KEV records; empty when nobody is known
			// to exploit it.
			KnownExploited []json.RawMessage `json:"knownExploited"`

			// Fix is the advisory's fix state and fixed versions.
			Fix struct {
				// State is "fixed" when a fixed version exists.
				State string `json:"state"`

				// Versions are the versions that fix the advisory.
				Versions []string `json:"versions"`
			} `json:"fix"`
		} `json:"vulnerability"`
	} `json:"matches"`
}

// GrypeEmergencies returns the findings in a grype JSON report that warrant blocking a
// deploy: every match that is known to be exploited and has a fixed version. Everything
// else — however severe — is reported by the scan but does not block, because an
// un-exploited advisory is not an emergency and an exploited one with no fix cannot be
// cleared by an upgrade. A report that is not valid grype JSON is a CodeInvalidInput
// error.
func GrypeEmergencies(report []byte) ([]Emergency, error) {
	var r grypeReport
	if err := json.Unmarshal(report, &r); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse grype JSON report")
	}
	var out []Emergency
	for i := range r.Matches {
		m := &r.Matches[i]
		v := &m.Vulnerability
		if len(v.KnownExploited) == 0 || v.Fix.State != "fixed" ||
			len(v.Fix.Versions) == 0 {
			continue
		}
		out = append(out, Emergency{
			ID:       v.ID,
			Severity: v.Severity,
			Package:  m.Artifact.Name,
			Version:  m.Artifact.Version,
			FixedIn:  v.Fix.Versions[0],
		})
	}
	return out, nil
}

// CheckGrypeReport is the image gate's verdict over the grype JSON report at path: nil
// when it holds no emergency (GrypeEmergencies), otherwise a CodeUpstream error listing
// each one as "<id> <package> <version> (fixed in <version>)". A report that cannot be
// read fails too, so a grype that wrote nothing never passes the gate.
func CheckGrypeReport(path string) error {
	report, err := os.ReadFile(path)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeNotFound, "read grype report "+path)
	}
	found, err := GrypeEmergencies(report)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	lines := make([]string, 0, len(found))
	for _, e := range found {
		lines = append(lines, fmt.Sprintf("%s %s %s (fixed in %s)",
			e.ID, e.Package, e.Version, e.FixedIn))
	}
	return apperr.New(apperr.CodeUpstream,
		"known-exploited vulnerabilities with a fix: "+strings.Join(lines, "; "))
}
