package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/imagescan"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// grypeReport is a grype `-o json` report with one match per gate case: a
// known-exploited finding with a fix (an emergency), a known-exploited finding with no
// fix, and a critical finding with a fix that nobody is known to exploit.
const grypeReport = `{"matches":[
 {"vulnerability":{"id":"CVE-2026-0001","severity":"High",
   "fix":{"versions":["1.2.4"],"state":"fixed"},
   "knownExploited":[{"cve":"CVE-2026-0001","knownRansomwareCampaignUse":"Unknown"}]},
  "artifact":{"name":"libexploited","version":"1.2.3"}},
 {"vulnerability":{"id":"CVE-2026-0002","severity":"Critical",
   "fix":{"versions":[],"state":"not-fixed"},
   "knownExploited":[{"cve":"CVE-2026-0002"}]},
  "artifact":{"name":"libnofix","version":"0.9.0"}},
 {"vulnerability":{"id":"GO-2026-6604","severity":"Critical",
   "fix":{"versions":["1.27.3"],"state":"fixed"}},
  "artifact":{"name":"stdlib","version":"go1.27.0"}}
]}`

// TestGrypeEmergencies tests the image gate's emergency policy over a grype JSON report.
//
// Why this test is important:
//   - The image gate blocks a deploy only in an emergency: a vulnerability someone is
//     known to be exploiting (CISA KEV) that an upgrade can fix. Blocking on severity
//     alone stopped deploys for un-exploited advisories; blocking on an exploited finding
//     with no fix would block a deploy nobody can unblock.
//
// What it tests:
//   - Only the known-exploited, fixed match is returned, with its package, installed
//     version, and fix version; the un-fixed exploited match and the critical
//     un-exploited match are not.
//   - A report with no matches yields no emergencies, and malformed JSON is a coded
//     error.
func TestGrypeEmergencies(t *testing.T) {
	t.Parallel()

	got, err := imagescan.GrypeEmergencies([]byte(grypeReport))
	require.NoError(t, err)
	assert.Equal(t, []imagescan.Emergency{{
		ID: "CVE-2026-0001", Severity: "High",
		Package: "libexploited", Version: "1.2.3", FixedIn: "1.2.4",
	}}, got)

	none, err := imagescan.GrypeEmergencies([]byte(`{"matches":[]}`))
	require.NoError(t, err)
	assert.Empty(t, none)

	_, err = imagescan.GrypeEmergencies([]byte(textNotStructured))
	require.Error(t, err)
}

// TestCheckGrypeReport tests the gate verdict over a grype JSON report file.
//
// Why this test is important:
//   - An image gate blocks through this one check, so it must fail on an emergency,
//     pass on severe-but-unexploited findings, and never pass when the report is
//     missing (a grype that wrote nothing scanned nothing).
//
// What it tests:
//   - A report with a known-exploited fixed match fails, naming the advisory, package,
//     and fix version.
//   - A report with only un-exploited findings passes.
//   - A missing report file fails.
func TestCheckGrypeReport(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(grypeReport), writeFileInput))

	err := imagescan.CheckGrypeReport(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CVE-2026-0001 libexploited 1.2.3 (fixed in 1.2.4)")

	clean := filepath.Join(dir, "clean.json")
	require.NoError(t, os.WriteFile(clean, []byte(`{"matches":[
	 {"vulnerability":{"id":"GO-2026-6604","severity":"Critical",
	   "fix":{"versions":["1.27.3"],"state":"fixed"}},
	  "artifact":{"name":"stdlib","version":"go1.27.0"}}]}`), writeFileInput))
	require.NoError(t, imagescan.CheckGrypeReport(clean))

	require.Error(t, imagescan.CheckGrypeReport(filepath.Join(dir, "missing.json")))
}

// scanPolicy is the Trivy policy the ScanImage cases run under.
func scanPolicy() imagescan.TrivyPolicy {
	return imagescan.TrivyPolicy{
		Scanners: "vuln", Severity: "HIGH,CRITICAL", IgnoreFile: ".trivyignore",
		IgnoreUnfixed: true,
		ReportOnly:    false,
	}
}

// TestScanImage tests the Trivy image gate.
//
// Why this test is important:
//   - CI and a deploy gate images on this scan: its argv carries the policy, a finding
//     must fail it unless another verdict decides, and a missing or misconfigured trivy
//     must never pass a strict gate silently.
//
// What it tests (mocked runner — no subprocess):
//   - The argv carries the scanners, severities, --ignore-unfixed, the ignore file,
//     --exit-code 1 and the ref; ReportOnly runs --exit-code 0.
//   - A trivy failure is a Fail row naming the ref; a clean scan passes.
//   - A missing trivy skips, or fails CodeNotFound under strict; an empty policy fails.
func TestScanImage(t *testing.T) {
	t.Parallel()

	const ref = "registry.example/shelf@sha256:abc"

	want := []string{
		textImage, "--scanners", "vuln", "--severity", "HIGH,CRITICAL",
		"--ignore-unfixed", "--ignorefile", ".trivyignore", "--exit-code", "1", ref,
	}

	t.Run("clean and failing scans", func(t *testing.T) {
		t.Parallel()

		runner := mocks.NewMockCommandRunner(gomock.NewController(t))
		runner.EXPECT().Exists(textTrivy).Return(true).Times(2)
		runner.EXPECT().Run(gomock.Any(), textRepo, textTrivy, toAny(want)...).Return(nil)
		runner.EXPECT().Run(gomock.Any(), textRepo, textTrivy, toAny(want)...).
			Return(assert.AnError)

		ok := imagescan.ScanImageStrict(t.Context(), runner, textRepo, scanPolicy(), ref)
		assert.Equal(t, types.StatusPass, ok[0].Status)

		bad := imagescan.ScanImageStrict(t.Context(), runner, textRepo, scanPolicy(), ref)
		assert.Equal(t, types.StatusFail, bad[0].Status)
		assert.Contains(t, bad[0].Error, ref)
	})

	t.Run("report only", func(t *testing.T) {
		t.Parallel()

		p := scanPolicy()

		p.ReportOnly = true

		args := p.Args(ref)
		assert.Equal(t, text0, args[len(args)-2])
	})

	t.Run("missing trivy and empty policy", func(t *testing.T) {
		t.Parallel()

		assertScanWithoutTrivy(t, ref)
	})
}

// toAny widens args for a variadic gomock expectation.
func toAny(args []string) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}

	return out
}

// assertScanWithoutTrivy pins the outcomes when trivy is absent or the policy is empty.
func assertScanWithoutTrivy(t *testing.T, ref string) {
	t.Helper()

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().Exists(textTrivy).Return(false).Times(wantCalls)

	skip := imagescan.ScanImage(
		t.Context(),
		runner,
		textRepo,
		scanPolicy(),
		ref)
	assert.Equal(t, types.StatusSkip, skip[0].Status)

	strict := imagescan.ScanImageStrict(
		t.Context(),
		runner,
		textRepo,
		scanPolicy(),
		ref)
	assert.Equal(t, types.StatusFail, strict[0].Status)

	present := mocks.NewMockCommandRunner(gomock.NewController(t))
	present.EXPECT().Exists(textTrivy).Return(true)

	empty := imagescan.ScanImageStrict(t.Context(), present, textRepo,
		stdx.Zero[imagescan.TrivyPolicy](), ref)
	assert.Equal(t, types.StatusFail, empty[0].Status)
}
