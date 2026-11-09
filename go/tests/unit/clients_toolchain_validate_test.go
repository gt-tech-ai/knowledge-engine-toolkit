package unit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// pinnedRelease returns a well-formed, checksum-pinned github-release method.
func pinnedRelease() types.InstallMethodEntry {
	return types.InstallMethodEntry{
		Kind: types.InstallKindGitHubRelease, Org: textO, Repo: "r", Tag: textVLower,
		Version: textVLower, Binary: textX,
		Assets: map[string]types.AssetEntry{
			textLinuxAmd64: {
				Name:   "x_linux_amd64.tar.gz",
				SHA256: strings.Repeat(textA, repeatInput),
			},
		},
	}
}

// pinnedURL returns a well-formed, checksum-pinned url-kind method.
func pinnedURL() types.InstallMethodEntry {
	return types.InstallMethodEntry{
		Kind: types.InstallKindURL, URL: "https://example.test/dl", Version: textVLower,
		Binary: textX,
		Assets: map[string]types.AssetEntry{
			textDarwinArm64: {
				Name:   "x-darwin-arm64",
				SHA256: strings.Repeat(textB, repeatInput),
			},
		},
	}
}

// toolWith wraps one install method (on the "all" platform) in an optional tool.
func toolWith(m *types.InstallMethodEntry) types.ToolEntry {
	return types.ToolEntry{
		Name: textX, Binary: textX, Category: types.CategoryOptional, CI: true,
		Install: map[string]types.InstallMethodEntry{textAll: *m},
	}
}

// TestClientsToolchainValidateToolEntries tests manifest validation.
//
// Why this test is important:
//   - A malformed tool manifest fails every install that reads it, so validation must
//     reject bad entries at load time, before they ship.
//
// What it tests:
//   - Well-formed lifecycle and registry-only entries pass.
//   - Empty binary, unknown category, unknown version_parse, unknown install
//     kind, and each kind's missing-required-field case are all rejected.
//   - A download kind (github-release / url) must pin a version and at least one
//     asset, each for a known os-arch with a name and a 64-hex SHA-256, and a
//     binary; a url must be https; and only a required/optional tool may carry the
//     `ci` marker (not a registry-only tool, not a prerequisite).
//   - NewCatalog rejects two entries with one binary as INVALID_INPUT: a lookup by
//     binary would silently pick one.
//   - Each rejection names the rule that fired, so a case can't pass for another reason.
func TestClientsToolchainValidateToolEntries(t *testing.T) {
	t.Parallel()

	good := types.ToolEntry{
		Name: textGrpcurl, Binary: textGrpcurl, Category: types.CategoryOptional,
		VersionParse: "first-version",
		Install: map[string]types.InstallMethodEntry{
			textAll: {Kind: types.InstallKindGoInstall, Package: "x/y@v1"},
		},
	}

	cases := testClientsToolchainValidateToolEntriesRows(&good)

	_, dup := toolchain.NewCatalog([]types.ToolEntry{good, good})
	require.Error(t, dup)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(dup))
	assert.Contains(t, dup.Error(), `duplicate binary "grpcurl"`)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := apperr.Join(types.ValidateToolEntries(
				[]types.ToolEntry{testCase.tool})...)
			if testCase.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr, "the rule that fired")
		})
	}
}

// testClientsToolchainValidateToolEntriesRow is one row of
// testClientsToolchainValidateToolEntriesTable.
type testClientsToolchainValidateToolEntriesRow struct {
	name    string
	wantErr string // "" = valid
	tool    types.ToolEntry
}

// testClientsToolchainValidateToolEntriesRows builds the table
// testClientsToolchainValidateToolEntries runs.
func testClientsToolchainValidateToolEntriesRows(
	good *types.ToolEntry,
) []testClientsToolchainValidateToolEntriesRow {
	return slices.Concat(
		validateEntryShapeRows(good),
		validateInstallKindRows(),
		validateDownloadRows(),
		validateAssetRows(),
		validateMarkerRows(),
	)
}

// validateEntryShapeRows lists the entry-level rows.
func validateEntryShapeRows(
	good *types.ToolEntry,
) []testClientsToolchainValidateToolEntriesRow {
	return []testClientsToolchainValidateToolEntriesRow{
		{name: "valid lifecycle tool", wantErr: "", tool: *good},
		{name: "registry-only tool", wantErr: "", tool: types.ToolEntry{
			Name:   textRuff,
			Binary: textRuff,
			Cmd:    []string{textUv, textRun, textRuff},
		}},
		{
			name:    "empty binary",
			wantErr: "has empty binary",
			tool:    types.ToolEntry{Name: textX},
		},
		{
			name:    "unknown category",
			wantErr: "has unknown category",
			tool:    types.ToolEntry{Binary: textX, Category: textBogus},
		},
		{
			name:    "unknown version_parse",
			wantErr: "has unknown version_parse",
			tool:    types.ToolEntry{Binary: textX, VersionParse: textBogus},
		},
		{
			name:    "unknown install kind",
			wantErr: `unknown install kind "magic"`,
			tool: types.ToolEntry{
				Binary: textX,
				Install: map[string]types.InstallMethodEntry{
					textAll: {Kind: "magic"},
				},
			},
		},
	}
}

// validateInstallKindRows lists the install-kind rows.
func validateInstallKindRows() []testClientsToolchainValidateToolEntriesRow {
	return []testClientsToolchainValidateToolEntriesRow{
		{
			name:    "command kind missing command",
			wantErr: "command install kind requires a command",
			tool: types.ToolEntry{
				Binary: textX,
				Install: map[string]types.InstallMethodEntry{
					textAll: {Kind: types.InstallKindCommand},
				},
			},
		},
		{
			name:    "go-install missing package",
			wantErr: "go-install kind requires a package",
			tool: types.ToolEntry{
				Binary: textX,
				Install: map[string]types.InstallMethodEntry{
					textAll: {Kind: types.InstallKindGoInstall},
				},
			},
		},
		{
			name:    "github-release missing repo",
			wantErr: "github-release kind requires org, repo, tag",
			tool: types.ToolEntry{
				Binary: textX,
				Install: map[string]types.InstallMethodEntry{
					textWindows: func() types.InstallMethodEntry {
						m := pinnedRelease()

						m.Repo = ""

						return m
					}(),
				},
			},
		},
	}
}

// validateDownloadRows lists the pinned-download rows.
func validateDownloadRows() []testClientsToolchainValidateToolEntriesRow {
	return []testClientsToolchainValidateToolEntriesRow{
		{
			name:    "pinned github-release",
			wantErr: "",
			tool:    toolWith(new(pinnedRelease())),
		},
		{name: "pinned url download", wantErr: "", tool: toolWith(new(pinnedURL()))},
		{
			name:    "download without a version",
			wantErr: textRequiresVersionBinaryAnd,
			tool: toolWith(new(func() types.InstallMethodEntry {
				m := pinnedURL()

				m.Version = ""

				return m
			}())),
		},
		{
			name:    "download without assets",
			wantErr: textRequiresVersionBinaryAnd,
			tool: toolWith(new(func() types.InstallMethodEntry {
				m := pinnedRelease()

				m.Assets = nil

				return m
			}())),
		},
	}
}

// validateAssetRows lists the asset rows.
func validateAssetRows() []testClientsToolchainValidateToolEntriesRow {
	return []testClientsToolchainValidateToolEntriesRow{
		{
			name:    "asset with a malformed sha256",
			wantErr: "needs a name and a 64-hex sha256",
			tool: toolWith(new(func() types.InstallMethodEntry {
				installMethodEntry := pinnedURL()

				installMethodEntry.Assets = map[string]types.AssetEntry{
					textLinuxAmd64: {Name: "x.tar.gz", SHA256: "not-a-digest"},
				}

				return installMethodEntry
			}())),
		},
		{
			name:    "asset for an unknown platform",
			wantErr: "is not an os-arch key",
			tool: toolWith(new(func() types.InstallMethodEntry {
				currentInstallMethodEntry := pinnedURL()

				currentInstallMethodEntry.Assets = map[string]types.AssetEntry{
					"plan9-mips": {Name: textX, SHA256: strings.Repeat(textA, 64)},
				}

				return currentInstallMethodEntry
			}())),
		},
		{
			name:    "url download over plain http",
			wantErr: "requires an https:// url",
			tool: toolWith(new(func() types.InstallMethodEntry {
				m := pinnedURL()

				m.URL = "http://example.test/dl"

				return m
			}())),
		},
		{
			name:    "ci marker on a registry-only tool",
			wantErr: "is ci but not a required/optional tool",
			tool:    types.ToolEntry{Name: textRuff, Binary: textRuff, CI: true},
		},
	}
}

// validateMarkerRows lists the remaining asset, download and CI-marker rows.
func validateMarkerRows() []testClientsToolchainValidateToolEntriesRow {
	return []testClientsToolchainValidateToolEntriesRow{
		{
			name:    "asset without a name",
			wantErr: "needs a name and a 64-hex sha256",
			tool: toolWith(new(func() types.InstallMethodEntry {
				installMethodEntry := pinnedURL()

				installMethodEntry.Assets = map[string]types.AssetEntry{
					textLinuxAmd64: {SHA256: strings.Repeat(textA, 64)},
				}

				return installMethodEntry
			}())),
		},
		{
			name:    "download without a binary",
			wantErr: textRequiresVersionBinaryAnd,
			tool: toolWith(new(func() types.InstallMethodEntry {
				m := pinnedRelease()

				m.Binary = ""

				return m
			}())),
		},
		{
			name:    "ci marker on a prerequisite",
			wantErr: "is ci but not a required/optional tool",
			tool: types.ToolEntry{
				Name:     textGoLower,
				Binary:   textGoLower,
				CI:       true,
				Category: types.CategoryPrerequisite,
			},
		},
	}
}

const (
	// repeatInput is the repeat input the tests use.
	repeatInput = 64
)

const (
	// textRequiresVersionBinaryAnd is the literal "requires version, binary, and assets".
	textRequiresVersionBinaryAnd = "requires version, binary, and assets"

	// textVLower is the literal "v1.0.0".
	textVLower = "v1.0.0"
)
