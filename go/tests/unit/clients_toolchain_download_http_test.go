package unit_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestClientsToolchainInstall_DefaultDownloaderFetchesOverHTTP tests the production
// artifact downloader the Installer uses when no Downloader is injected.
//
// Why this test is important:
//   - Every other install test injects a mock downloader, so the real HTTP fetch —
//     the one CI and setup run — was untested. An error page served with a
//     non-200 status must never be taken for the artifact.
//
// What it tests:
//   - Against a local httptest server: a 200 body whose SHA-256 matches is placed
//     in the install dir; a 404 fails the install and places nothing.
func TestClientsToolchainInstall_DefaultDownloaderFetchesOverHTTP(t *testing.T) {
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/pinned" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(checksumPayload())
		}),
	)
	defer srv.Close()

	for name, tc := range map[string]struct {
		path    string
		wantErr bool
	}{
		"200 with a matching body": {path: "/pinned"},
		"404 error page":           {path: "/missing", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			binDir, _ := isolateInstallDirs(t)
			runner := mocks.NewMockCommandRunner(gomock.NewController(t))
			tool := downloadTool(map[string]types.Artifact{
				"linux-amd64": {
					URL:    srv.URL + tc.path,
					SHA256: sha256Hex(checksumPayload()),
				},
			})
			inst := toolchain.NewInstaller(
				runner,
				fixtures.NopLogger(),
				"linux-apt",
				denyPrompter(t),
				toolchain.WithArch("amd64"),
			)

			err := inst.Install(context.Background(), "/root", tool)

			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, dirEntries(t, binDir))
				return
			}
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(binDir, "pinned"))
			require.NoError(t, err)
			assert.Equal(t, checksumPayload(), got)
		})
	}
}

// TestClientsToolchainInstall_WindowsARM64FallsBackToAMD64Asset tests that a Windows
// ARM64 host installs a tool pinned only for windows-amd64.
//
// Why this test is important:
//   - Several required tools publish no windows-arm64 asset. Windows on ARM64 runs
//     x64 binaries under emulation, so refusing them would break setup on
//     those hosts for no benefit.
//
// What it tests:
//   - Platform windows, arch arm64, artifacts only for windows-amd64: the amd64 URL
//     is fetched, verified, and placed as pinned.exe.
func TestClientsToolchainInstall_WindowsARM64FallsBackToAMD64Asset(t *testing.T) {
	binDir, _ := isolateInstallDirs(t)
	ctrl := gomock.NewController(t)
	dl := mocks.NewMockDownloader(ctrl)
	const url = "https://example.test/pinned-windows-amd64.exe"
	dl.EXPECT().Fetch(gomock.Any(), url).
		Return(io.NopCloser(bytes.NewReader(checksumPayload())), nil)
	tool := downloadTool(map[string]types.Artifact{
		"windows-amd64": {URL: url, SHA256: sha256Hex(checksumPayload())},
	})
	inst := toolchain.NewInstaller(
		mocks.NewMockCommandRunner(ctrl),
		fixtures.NopLogger(),
		"windows",
		denyPrompter(t),
		toolchain.WithArch("arm64"),
		toolchain.WithDownloader(dl),
	)

	require.NoError(t, inst.Install(context.Background(), "/root", tool))
	assert.Equal(t, []string{"pinned.exe"}, dirEntries(t, binDir))
}
