package unit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/pins"
)

// pinsEcosystems is a codebase with one marker file per ecosystem, plus markers that
// must not count: a vendored subchart and files under node_modules, vendor and .venv.
func pinsEcosystems() map[string]string {
	return map[string]string{
		"go.work":                            "go 1.27\n\nuse ./svc\n",
		"go.mod":                             "module example.com/mono\n\ngo 1.27\n",
		"py/lib/pyproject.toml":              "[project]\nname = \"x\"\n",
		"web/package.json":                   "{}\n",
		"docker/api/Dockerfile":              "FROM scratch\n",
		"docker/compose.yaml":                "services: {}\n",
		".github/workflows/ci.yaml":          "name: ci\n",
		"infra/main.tf":                      "terraform {}\n",
		"infra/live/terragrunt.hcl":          "terraform {}\n",
		"deploy/api/Chart.yaml":              "name: api\n",
		"deploy/api/charts/redis/Chart.yaml": "name: redis\n",
	}
}

// pinsPolicy is a full update policy with one gated entry of each kind.
func pinsPolicy() pins.Policy {
	return pins.Policy{
		Timezone:            "UTC",
		Extends:             []string{"config:recommended"},
		Labels:              []string{"dependencies"},
		Schedule:            []string{"before 6am on Monday"},
		PRHourlyLimit:       2,
		PRConcurrentLimit:   5,
		LockFileMaintenance: &pins.LockFileMaintenance{Enabled: true},
		VulnerabilityAlerts: &pins.VulnerabilityAlerts{
			Enabled: true, Labels: []string{"security"},
		},
		GroupRules: []pins.Gated[pins.PackageRule]{{
			When:  []pins.Manager{pins.ManagerDockerCompose},
			Value: pins.PackageRule{GroupName: "Base images"},
		}},
		PinRules: []pins.Gated[pins.PackageRule]{{
			When:  []pins.Manager{pins.ManagerGomod},
			Value: pins.PackageRule{MatchDatasources: []string{"golang-version"}},
		}},
		CustomManagers: []pins.Gated[pins.CustomManager]{{
			When:  []pins.Manager{pins.ManagerGomod, pins.ManagerGithubActions},
			Value: pins.CustomManager{CustomType: "regex", DatasourceTemplate: "go"},
		}},
		IgnorePaths: []pins.Gated[string]{{
			When: []pins.Manager{pins.ManagerHelmv3}, Value: "deploy/**/charts/**",
		}},
	}
}

// TestFoundationPins_Detect tests ecosystem detection.
//
// Why this test is important:
//   - The config enables a manager only for an ecosystem the codebase holds: a missed
//     marker leaves its dependencies unmanaged, and a marker counted from a vendored
//     or dependency directory enables a manager with nothing of ours to update.
//
// What it tests:
//   - Every ecosystem is detected from its marker and returned sorted, with the
//     default ignore paths and the root module path; markers under node_modules,
//     vendor, .venv and a charts/ directory count for nothing; an empty codebase
//     has no managers.
func TestFoundationPins_Detect(t *testing.T) {
	t.Parallel()
	got, err := pins.Detect(t.Context(), memoryCodebase(t, pinsEcosystems()))
	require.NoError(t, err)
	assert.Equal(t, []pins.Manager{
		pins.ManagerDockerCompose, pins.ManagerDockerfile, pins.ManagerGithubActions,
		pins.ManagerGomod, pins.ManagerHelmv3, pins.ManagerNpm, pins.ManagerPEP621,
		pins.ManagerTerraform, pins.ManagerTerragrunt,
	}, got.Managers)
	assert.Equal(t, []string{"**/node_modules/**", ".venv/**"}, got.IgnorePaths)
	assert.Equal(t, "example.com/mono", got.GoModule)

	got, err = pins.Detect(t.Context(), memoryCodebase(t, map[string]string{
		"web/node_modules/x/package.json": "{}",
		"vendor/x/Dockerfile":             "FROM x",
		".venv/lib/pyproject.toml":        "",
		"charts/redis/Chart.yaml":         "name: redis",
		".github/actions/x/action.yaml":   "name: x",
	}))
	require.NoError(t, err)
	assert.Equal(t, []pins.Manager{pins.ManagerGithubActions}, got.Managers,
		"only the composite action counts")
	assert.Empty(t, got.GoModule)
}

// pinsGolden is the exact config for {github-actions, gomod} under pinsPolicy.
const pinsGolden = `{
  "lockFileMaintenance": {
    "enabled": true
  },
  "vulnerabilityAlerts": {
    "labels": [
      "security"
    ],
    "enabled": true
  },
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "timezone": "UTC",
  "extends": [
    "config:recommended"
  ],
  "labels": [
    "dependencies"
  ],
  "schedule": [
    "before 6am on Monday"
  ],
  "packageRules": [
    {
      "matchManagers": [
        "github-actions"
      ],
      "matchUpdateTypes": [
        "minor",
        "patch"
      ],
      "groupName": "GitHub Actions",
      "automerge": true,
      "automergeType": "pr"
    },
    {
      "matchManagers": [
        "gomod"
      ],
      "groupName": "Go dependencies",
      "postUpdateOptions": [
        "gomodTidy"
      ],
      "automerge": false
    },
    {
      "matchUpdateTypes": [
        "major"
      ],
      "matchPackageNames": [
        "*"
      ],
      "automerge": false,
      "labels": [
        "dependencies",
        "major"
      ]
    },
    {
      "matchDatasources": [
        "golang-version"
      ]
    },
    {
      "matchManagers": [
        "gomod"
      ],
      "matchPackageNames": [
        "example.com/mono/**"
      ],
      "enabled": false
    }
  ],
  "customManagers": [
    {
      "customType": "regex",
      "datasourceTemplate": "go",
      "managerFilePatterns": null,
      "matchStrings": null
    }
  ],
  "ignorePaths": [
    "**/node_modules/**",
    ".venv/**"
  ],
  "prHourlyLimit": 2,
  "prConcurrentLimit": 5
}
`

// TestFoundationPins_Render tests the rendered config.
//
// Why this test is important:
//   - The committed config is this renderer's output under a byte-for-byte drift
//     check, so its format and rule order are the contract; Renovate merges matching
//     rules in order, so a pin rule placed before the rules it overrides would not hold.
//
// What it tests:
//   - Render emits exactly pinsGolden: managers sorted, each manager's default rule,
//     the gated policy entries whose managers are present (and not the group rule
//     gated on Compose or the Helm ignore path), the major rule, the pin rule, the
//     workspace-module rule last; the output is identical across runs.
func TestFoundationPins_Render(t *testing.T) {
	t.Parallel()
	e := pins.EcosystemSet{
		Managers:    []pins.Manager{pins.ManagerGomod, pins.ManagerGithubActions},
		IgnorePaths: []string{".venv/**", "**/node_modules/**"},
		GoModule:    "example.com/mono",
	}
	got, err := pins.Render(e, pinsPolicy())
	require.NoError(t, err)
	assert.Equal(t, strings.Split(pinsGolden, "\n"), strings.Split(string(got), "\n"),
		"byte-for-byte, line by line")

	again, err := pins.Render(e, pinsPolicy())
	require.NoError(t, err)
	assert.Equal(t, got, again)

	cfg := pins.Build(pins.EcosystemSet{
		Managers: []pins.Manager{pins.ManagerDockerCompose, pins.ManagerHelmv3, "bogus"},
	}, pinsPolicy())
	require.Len(t, cfg.PackageRules, 4, "two managers, the group rule, the major rule")
	assert.Equal(t, "Base images", cfg.PackageRules[2].GroupName)
	assert.Equal(t, []string{"deploy/**/charts/**"}, cfg.IgnorePaths)
	assert.Empty(t, cfg.CustomManagers)
}

// TestFoundationPins_Generator tests the config as a generator.
//
// Why this test is important:
//   - The drift check is what keeps the committed config current: a new ecosystem
//     must show up as drift until the config is regenerated.
//
// What it tests:
//   - A codebase holding the rendered config at the default path is in sync; adding
//     a Dockerfile makes renovate.json drift.
func TestFoundationPins_Generator(t *testing.T) {
	t.Parallel()
	g := pins.NewGenerator("", pinsPolicy())
	assert.Equal(t, "renovate", g.Name())
	files := map[string]string{"go.mod": "module example.com/mono\n"}
	out, err := g.Render(t.Context(), memoryCodebase(t, files))
	require.NoError(t, err)
	files[pins.ConfigFileName] = string(out[pins.ConfigFileName])

	d, err := gen.Check(t.Context(), memoryCodebase(t, files), g)
	require.NoError(t, err)
	assert.True(t, d.InSync, d.Diff)

	files["Dockerfile"] = "FROM scratch\n"
	d, err = gen.Check(t.Context(), memoryCodebase(t, files), g)
	require.NoError(t, err)
	assert.Equal(t, []string{pins.ConfigFileName}, d.Paths)
}
