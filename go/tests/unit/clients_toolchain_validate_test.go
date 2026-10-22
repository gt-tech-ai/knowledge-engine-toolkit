package unit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// pinnedRelease returns a well-formed, checksum-pinned github-release method.
func pinnedRelease() toolchain.InstallMethodEntry {
	return toolchain.InstallMethodEntry{
		Kind: toolchain.InstallKindGitHubRelease, Org: "o", Repo: "r", Tag: "v1.0.0",
		Version: "v1.0.0", Binary: "x",
		Assets: map[string]toolchain.AssetEntry{
			"linux-amd64": {
				Name:   "x_linux_amd64.tar.gz",
				SHA256: strings.Repeat("a", 64),
			},
		},
	}
}

// pinnedURL returns a well-formed, checksum-pinned url-kind method.
func pinnedURL() toolchain.InstallMethodEntry {
	return toolchain.InstallMethodEntry{
		Kind: toolchain.InstallKindURL, URL: "https://example.test/dl", Version: "v1.0.0",
		Binary: "x",
		Assets: map[string]toolchain.AssetEntry{
			"darwin-arm64": {Name: "x-darwin-arm64", SHA256: strings.Repeat("b", 64)},
		},
	}
}

// toolWith wraps one install method (on the "all" platform) in an optional tool.
func toolWith(m toolchain.InstallMethodEntry) toolchain.ToolEntry {
	return toolchain.ToolEntry{
		Name: "x", Binary: "x", Category: toolchain.CategoryOptional, CI: true,
		Install: map[string]toolchain.InstallMethodEntry{"all": m},
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

	good := toolchain.ToolEntry{
		Name: "grpcurl", Binary: "grpcurl", Category: toolchain.CategoryOptional,
		VersionParse: "first-version",
		Install: map[string]toolchain.InstallMethodEntry{
			"all": {Kind: toolchain.InstallKindGoInstall, Package: "x/y@v1"},
		},
	}

	cases := []struct {
		name    string
		wantErr string // "" = valid
		tool    toolchain.ToolEntry
	}{
		{name: "valid lifecycle tool", wantErr: "", tool: good},
		{name: "registry-only tool", wantErr: "", tool: toolchain.ToolEntry{
			Name:   "ruff",
			Binary: "ruff",
			Cmd:    []string{"uv", "run", "ruff"},
		}},
		{
			name:    "empty binary",
			wantErr: "has empty binary",
			tool:    toolchain.ToolEntry{Name: "x"},
		},
		{
			name:    "unknown category",
			wantErr: "has unknown category",
			tool:    toolchain.ToolEntry{Binary: "x", Category: "bogus"},
		},
		{
			name:    "unknown version_parse",
			wantErr: "has unknown version_parse",
			tool:    toolchain.ToolEntry{Binary: "x", VersionParse: "bogus"},
		},
		{
			name:    "unknown install kind",
			wantErr: `unknown install kind "magic"`,
			tool: toolchain.ToolEntry{
				Binary: "x",
				Install: map[string]toolchain.InstallMethodEntry{
					"all": {Kind: "magic"},
				},
			},
		},
		{
			name:    "command kind missing command",
			wantErr: "command install kind requires a command",
			tool: toolchain.ToolEntry{
				Binary: "x",
				Install: map[string]toolchain.InstallMethodEntry{
					"all": {Kind: toolchain.InstallKindCommand},
				},
			},
		},
		{
			name:    "go-install missing package",
			wantErr: "go-install kind requires a package",
			tool: toolchain.ToolEntry{
				Binary: "x",
				Install: map[string]toolchain.InstallMethodEntry{
					"all": {Kind: toolchain.InstallKindGoInstall},
				},
			},
		},
		{
			name:    "github-release missing repo",
			wantErr: "github-release kind requires org, repo, tag",
			tool: toolchain.ToolEntry{
				Binary: "x",
				Install: map[string]toolchain.InstallMethodEntry{
					"windows": func() toolchain.InstallMethodEntry {
						m := pinnedRelease()
						m.Repo = ""
						return m
					}(),
				},
			},
		},
		{name: "pinned github-release", wantErr: "", tool: toolWith(pinnedRelease())},
		{name: "pinned url download", wantErr: "", tool: toolWith(pinnedURL())},
		{
			name:    "download without a version",
			wantErr: "requires version, binary, and assets",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedURL()
				m.Version = ""
				return m
			}()),
		},
		{
			name:    "download without assets",
			wantErr: "requires version, binary, and assets",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedRelease()
				m.Assets = nil
				return m
			}()),
		},
		{
			name:    "asset with a malformed sha256",
			wantErr: "needs a name and a 64-hex sha256",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedURL()
				m.Assets = map[string]toolchain.AssetEntry{
					"linux-amd64": {Name: "x.tar.gz", SHA256: "not-a-digest"},
				}
				return m
			}()),
		},
		{
			name:    "asset for an unknown platform",
			wantErr: "is not an os-arch key",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedURL()
				m.Assets = map[string]toolchain.AssetEntry{
					"plan9-mips": {Name: "x", SHA256: strings.Repeat("a", 64)},
				}
				return m
			}()),
		},
		{
			name:    "url download over plain http",
			wantErr: "requires an https:// url",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedURL()
				m.URL = "http://example.test/dl"
				return m
			}()),
		},
		{
			name:    "ci marker on a registry-only tool",
			wantErr: "is ci but not a required/optional tool",
			tool:    toolchain.ToolEntry{Name: "ruff", Binary: "ruff", CI: true},
		},
		{
			name:    "asset without a name",
			wantErr: "needs a name and a 64-hex sha256",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedURL()
				m.Assets = map[string]toolchain.AssetEntry{
					"linux-amd64": {SHA256: strings.Repeat("a", 64)},
				}
				return m
			}()),
		},
		{
			name:    "download without a binary",
			wantErr: "requires version, binary, and assets",
			tool: toolWith(func() toolchain.InstallMethodEntry {
				m := pinnedRelease()
				m.Binary = ""
				return m
			}()),
		},
		{
			name:    "ci marker on a prerequisite",
			wantErr: "is ci but not a required/optional tool",
			tool: toolchain.ToolEntry{
				Name:     "go",
				Binary:   "go",
				CI:       true,
				Category: toolchain.CategoryPrerequisite,
			},
		},
	}

	_, dup := toolchain.NewCatalog([]toolchain.ToolEntry{good, good})
	require.Error(t, dup)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(dup))
	assert.Contains(t, dup.Error(), `duplicate binary "grpcurl"`)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := apperr.Join(toolchain.ValidateToolEntries(
				[]toolchain.ToolEntry{tc.tool})...)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr, "the rule that fired")
		})
	}
}
