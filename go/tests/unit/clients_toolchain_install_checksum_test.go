package unit_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// checksumPayload is the "binary" the mocked download serves.
func checksumPayload() []byte {
	return []byte("#!/bin/sh\necho pinned\n")
}

// sha256Hex returns the lowercase hex SHA-256 of b — the manifest's checksum form.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// downloadTool returns a tool whose only install method is a pinned download of
// the given per-platform artifacts.
func downloadTool(artifacts map[string]types.Artifact) *types.ToolDef {
	return &types.ToolDef{
		Name:   "pinned",
		Binary: "pinned",
		InstallMethods: map[string]types.InstallMethod{
			"all": {Download: &types.Download{
				Binary:    "pinned",
				Version:   "v1.2.3",
				Artifacts: artifacts,
			}},
		},
	}
}

// isolateInstallDirs points the install dir (GOBIN) and the process temp dir at
// fresh empty directories, so a test can assert that nothing — not even a
// partially written temp file — is left behind.
func isolateInstallDirs(t *testing.T) (binDir, tmpDir string) {
	t.Helper()
	binDir, tmpDir = t.TempDir(), t.TempDir()
	t.Setenv("GOBIN", binDir)
	t.Setenv("TMPDIR", tmpDir)
	return binDir, tmpDir
}

// dirEntries lists a directory's entry names.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// tarGz packs one file at name into a gzip-compressed tarball.
func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// gzOf gzip-compresses a single file's bytes (no tar wrapper) — the shape a
// release ships when the artifact is one bare gzipped binary (e.g. taplo).
func gzOf(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write(body)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// zipOf packs one file at name into a zip archive.
func zipOf(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write(body)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// TestClientsToolchainInstall_VerifiesSHA256BeforePlacingBinary tests the verified
// download path.
//
// Why this test is important:
//   - CI installs every downloaded tool through this path. The binary may only
//     land on PATH after its bytes match the manifest's pinned SHA-256, and it
//     must land as an executable at the tool's name, whatever the release packs.
//
// What it tests:
//   - For a raw binary, a .tar.gz (binary nested in a directory, or under a
//     platform-named member the artifact names), and a .zip, the
//     artifact for the host's os-arch (linux-amd64) is fetched through the
//     download seam, and the verified binary is placed in the install dir (GOBIN)
//     with the served content and an executable mode.
//   - The install dir holds only the binary afterwards (no temp files), the temp dir
//     is empty (the downloaded artifact is removed), and no command is run (the strict
//     runner mock has no expectations).
func TestClientsToolchainInstall_VerifiesSHA256BeforePlacingBinary(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		member   string
		artifact []byte
	}{
		{"raw binary", "https://example.test/pinned-linux-amd64", "", checksumPayload()},
		{
			"tar.gz archive",
			"https://example.test/pinned_linux_amd64.tar.gz", "",
			tarGz(t, "pinned-1.2.3/pinned", checksumPayload()),
		},
		{
			"tar.gz archive with a platform-named member",
			"https://example.test/pinned_linux_amd64.tgz", "pinned-linux-amd64",
			tarGz(t, "pinned-linux-amd64", checksumPayload()),
		},
		{
			"zip archive",
			"https://example.test/pinned_linux_amd64.zip", "",
			zipOf(t, "pinned", checksumPayload()),
		},
		{
			"gz single file",
			"https://example.test/pinned-linux-x86_64.gz", "",
			gzOf(t, checksumPayload()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binDir, tmpDir := isolateInstallDirs(t)
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockCommandRunner(ctrl)
			dl := mocks.NewMockDownloader(ctrl)
			dl.EXPECT().Fetch(gomock.Any(), tc.url).
				Return(io.NopCloser(bytes.NewReader(tc.artifact)), nil)

			tool := downloadTool(map[string]types.Artifact{
				"linux-amd64": {
					URL: tc.url, SHA256: sha256Hex(tc.artifact), Member: tc.member,
				},
				"darwin-arm64": {
					URL:    "https://example.test/other",
					SHA256: sha256Hex(nil),
				},
			})
			inst := toolchain.NewInstaller(
				runner,
				fixtures.NopLogger(),
				"linux-apt",
				denyPrompter(t),
				toolchain.WithArch("amd64"),
				toolchain.WithDownloader(dl),
			)

			require.NoError(t, inst.Install(context.Background(), "/root", tool))

			placed := filepath.Join(binDir, "pinned")
			got, err := os.ReadFile(placed)
			require.NoError(t, err)
			assert.Equal(t, checksumPayload(), got)
			info, err := os.Stat(placed)
			require.NoError(t, err)
			assert.NotZero(
				t,
				info.Mode().Perm()&0o111,
				"placed binary must be executable",
			)
			assert.Equal(t, []string{"pinned"}, dirEntries(t, binDir))
			assert.Empty(t, dirEntries(t, tmpDir), "the verified download is cleaned up")
		})
	}
}

// TestClientsToolchainInstall_LeavesNothingWhenTheArtifactIsUnusable tests the download
// failures that happen after (or instead of) the checksum.
//
// Why this test is important:
//   - A verified archive that lacks the binary fails only after a file was staged in
//     the install dir; a half-written staged file there would be picked up as if it were
//     installed. A fetch failure must fail the install without touching the install dir.
//
// What it tests:
//   - A checksum-matching .tar.gz whose only entry is another file fails naming the
//     missing "pinned" entry, and both the install dir and the temp dir are empty.
//   - A failing Fetch fails the install and leaves the install dir empty.
func TestClientsToolchainInstall_LeavesNothingWhenTheArtifactIsUnusable(t *testing.T) {
	url := "https://example.test/pinned_linux_amd64.tar.gz"
	install := func(t *testing.T, dl *mocks.MockDownloader, sha string) error {
		t.Helper()
		tool := downloadTool(
			map[string]types.Artifact{"linux-amd64": {URL: url, SHA256: sha}},
		)
		inst := toolchain.NewInstaller(
			mocks.NewMockCommandRunner(gomock.NewController(t)),
			fixtures.NopLogger(),
			"linux-apt",
			denyPrompter(t),
			toolchain.WithArch("amd64"),
			toolchain.WithDownloader(dl),
		)
		return inst.Install(context.Background(), "/root", tool)
	}

	t.Run("verified archive without the binary", func(t *testing.T) {
		binDir, tmpDir := isolateInstallDirs(t)
		other := tarGz(t, "other/notpinned", checksumPayload())
		dl := mocks.NewMockDownloader(gomock.NewController(t))
		dl.EXPECT().
			Fetch(gomock.Any(), url).
			Return(io.NopCloser(bytes.NewReader(other)), nil)

		err := install(t, dl, sha256Hex(other))

		require.Error(t, err)
		assert.Contains(t, err.Error(), `has no "pinned" entry`)
		assert.Empty(
			t,
			dirEntries(t, binDir),
			"no staged .pinned.tmp-* file is left behind",
		)
		assert.Empty(t, dirEntries(t, tmpDir))
	})

	t.Run("fetch fails", func(t *testing.T) {
		binDir, _ := isolateInstallDirs(t)
		dl := mocks.NewMockDownloader(gomock.NewController(t))
		dl.EXPECT().
			Fetch(gomock.Any(), url).
			Return(nil, apperr.New(apperr.CodeInternal, "dial tcp: timeout"))

		err := install(t, dl, sha256Hex(checksumPayload()))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "dial tcp: timeout")
		assert.Empty(t, dirEntries(t, binDir))
	})
}

// TestClientsToolchainInstall_FailsAndLeavesNothingOnChecksumMismatch tests the tamper
// path.
//
// Why this test is important:
//   - A changed upstream artifact must never execute: a mismatch has to fail the
//     install before anything reaches PATH, and a half-written file left in the
//     install dir would be picked up by the next step as if it were installed.
//
// What it tests:
//   - When the served bytes differ from the pinned SHA-256, Install returns an
//     error naming the mismatch, and both the install dir and the temp dir are
//     empty afterwards.
func TestClientsToolchainInstall_FailsAndLeavesNothingOnChecksumMismatch(t *testing.T) {
	binDir, tmpDir := isolateInstallDirs(t)
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	dl := mocks.NewMockDownloader(ctrl)
	url := "https://example.test/pinned-linux-amd64"
	dl.EXPECT().Fetch(gomock.Any(), url).
		Return(io.NopCloser(bytes.NewReader([]byte("tampered upstream binary"))), nil)

	tool := downloadTool(map[string]types.Artifact{
		"linux-amd64": {URL: url, SHA256: sha256Hex(checksumPayload())},
	})
	inst := toolchain.NewInstaller(
		runner,
		fixtures.NopLogger(),
		"linux-apt",
		denyPrompter(t),
		toolchain.WithArch("amd64"),
		toolchain.WithDownloader(dl),
	)

	err := inst.Install(context.Background(), "/root", tool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 mismatch")
	assert.Empty(t, dirEntries(t, binDir), "a mismatched artifact must not be installed")
	assert.Empty(t, dirEntries(t, tmpDir), "the rejected download must be cleaned up")
}

// TestClientsToolchainInstall_FailsWhenPlatformChecksumMissing tests the
// unpinned-platform path.
//
// Why this test is important:
//   - A platform the manifest has no checksum for must not fall back to an
//     unverified download; failing before the fetch keeps "pinned" meaning pinned
//     on every host.
//
// What it tests:
//   - With no artifact for the host's os-arch, or an artifact with an empty
//     SHA-256, Install returns an error naming the platform, never calls the
//     download seam (strict mock), and leaves the install dir empty.
func TestClientsToolchainInstall_FailsWhenPlatformChecksumMissing(t *testing.T) {
	cases := map[string]map[string]types.Artifact{
		"no artifact for the platform": {
			"darwin-arm64": {URL: "https://example.test/darwin", SHA256: sha256Hex(nil)},
		},
		"artifact without a checksum": {
			"linux-amd64": {URL: "https://example.test/linux"},
		},
	}
	for name, artifacts := range cases {
		t.Run(name, func(t *testing.T) {
			binDir, _ := isolateInstallDirs(t)
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockCommandRunner(ctrl)
			dl := mocks.NewMockDownloader(ctrl) // strict: no Fetch expected

			inst := toolchain.NewInstaller(
				runner,
				fixtures.NopLogger(),
				"linux-apt",
				denyPrompter(t),
				toolchain.WithArch("amd64"),
				toolchain.WithDownloader(dl),
			)

			err := inst.Install(context.Background(), "/root", downloadTool(artifacts))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "linux-amd64")
			assert.Empty(t, dirEntries(t, binDir))
		})
	}
}
