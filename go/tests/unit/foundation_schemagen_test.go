package unit_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/schemagen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// schemaInner is a nested section of schemaOuter.
type schemaInner struct {
	// Port is required.
	Port int `yaml:"port" jsonschema:"required"`
}

// schemaOuter is the struct the schema tests reflect.
type schemaOuter struct {
	// Inner is a nested section.
	Inner schemaInner `yaml:"inner"`

	// Wait is a duration.
	Wait time.Duration `yaml:"wait"`
}

// schemaOf reflects schemaOuter under opts and decodes the JSON into a map.
func schemaOf(t *testing.T, opts schemagen.Options) map[string]any {
	t.Helper()
	raw, err := schemagen.For[schemaOuter](opts)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(raw), "}\n"), "newline-terminated")
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TestFoundationSchemagen_For tests JSON Schema reflection.
//
// Why this test is important:
//   - A committed schema validates hand-written YAML: a misspelled key must be
//     rejected, a duration must accept its string form, and the field docs the
//     reader sees must come from the Go source.
//
// What it tests:
//   - For reflects the yaml keys with additionalProperties: false, only
//     jsonschema:"required" fields required, a duration as string|integer and the
//     descriptions from Comments; Root wraps the schema in a one-key document
//     requiring that key; AllowExtraProps lifts additionalProperties: false at every
//     depth.
func TestFoundationSchemagen_For(t *testing.T) {
	t.Parallel()
	pkg := reflect.TypeFor[schemaOuter]().PkgPath()
	s := schemaOf(t, schemagen.Options{Comments: map[string]string{
		pkg + ".schemaOuter.Wait": "Wait is a duration.",
	}})
	assert.Equal(t, false, s["additionalProperties"])
	props := s["properties"].(map[string]any)
	wait := props["wait"].(map[string]any)
	assert.Equal(t, "Wait is a duration.", wait["description"])
	assert.Equal(t, []any{
		map[string]any{"type": "string"}, map[string]any{"type": "integer"},
	}, wait["oneOf"])
	inner := props["inner"].(map[string]any)
	assert.Equal(t, []any{"port"}, inner["required"])
	assert.Nil(t, s["required"])

	wrapped := schemaOf(t, schemagen.Options{Root: "outer"})
	assert.Equal(t, []any{"outer"}, wrapped["required"])
	assert.Contains(t, wrapped["properties"], "outer")

	loose := schemagen.Reflect(schemaOuter{}, schemagen.Options{})
	schemagen.AllowExtraProps(loose)
	raw, err := schemagen.Marshal(loose)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"additionalProperties": false`)
}

// schemaSources is a Go config schema tree: two packages each declaring a Config with
// a Default constructor, a Kind with a String method, and a test file to ignore.
func schemaSources() map[string]string {
	return map[string]string{
		"schema/a/config.go": `package a

// Config configures a.
type Config struct {
	// Timeout bounds a call | in seconds.
	Timeout int ` + "`mapstructure:\"timeout\"`" + `

	Name string ` + "`mapstructure:\"name\"`" + `

	// Skip has no key.
	Skip string

	// Tags are composite.
	Tags map[string]string ` + "`mapstructure:\"tags,omitempty\"`" + `
}

// DefaultConfig returns the defaults.
func DefaultConfig() *Config {
	return &Config{Timeout: 5, Tags: map[string]string{
		"a": "b",
	}}
}
`,
		"schema/b/config.go": `package b

// Config configures b.
type Config struct {
	// Timeout bounds b.
	Timeout int ` + "`mapstructure:\"timeout\"`" + `
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config { return Config{Timeout: 9} }
`,
		"schema/b/config_test.go": "package b\n\ntype T struct {\n\tX int `mapstructure:\"x\"`\n}\n",
		"store/kind.go": `package store

// Kind names a store backend.
type Kind int

// String names the kind.
func (k Kind) String() string {
	switch k {
	case 0:
		return "memory"
	case 1:
		return "postgres"
	default:
		return "unknown"
	}
}
`,
	}
}

// TestFoundationSchemagen_ParseAndKinds tests the Go sources the docs come from.
//
// Why this test is important:
//   - The reference lists every config field with its key, default and doc, and every
//     Kind a factory accepts; a default leaking between packages that both declare a
//     Config, or a sentinel default arm listed as a valid kind, documents a value the
//     code does not use.
//
// What it tests:
//   - ParseGoSchema returns one section per mapstructure-tagged struct, sorted by
//     package-qualified title, with each field's type, key (options stripped), doc
//     and its own package's Default constructor value; untagged fields and test files
//     are skipped; a missing dir is no sections. ExtractGoKinds returns the String
//     method's case values without the default arm and skips an absent package.
//   - Comments keys type and field docs by the package's import path.
func TestFoundationSchemagen_ParseAndKinds(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, schemaSources())
	secs, err := schemagen.ParseGoSchema(t.Context(), cb, "schema")
	require.NoError(t, err)
	require.Len(t, secs, 2)
	assert.Equal(t, []string{"a.Config", "b.Config"}, []string{secs[0].Title(), secs[1].Title()})
	assert.Equal(t, []schemagen.Field{
		{Name: "Timeout", Type: "int", Key: "timeout", Default: "5",
			Description: "Timeout bounds a call | in seconds."},
		{Name: "Name", Type: "string", Key: "name"},
		{Name: "Tags", Type: "map[string]string", Key: "tags",
			Default: "map[string]string{\n\t\"a\": \"b\",\n}", Description: "Tags are composite."},
	}, secs[0].Fields)
	assert.Equal(t, "9", secs[1].Fields[0].Default)

	none, err := schemagen.ParseGoSchema(t.Context(), cb, "absent")
	require.NoError(t, err)
	assert.Empty(t, none)

	kinds, err := schemagen.ExtractGoKinds(t.Context(), cb, []schemagen.GoKindRef{
		{Label: "Store", Key: "store.kind", PackageDir: "store"},
		{Label: "Absent", Key: "x.kind", PackageDir: "absent"},
	})
	require.NoError(t, err)
	assert.Equal(t, []schemagen.KindEntry{{
		Label: "Store", Key: "store.kind", Source: "go",
		Values: []string{"memory", "postgres"},
	}}, kinds)

	docs, err := schemagen.Comments(t.Context(), cb, "schema", "example.com/cfg")
	require.NoError(t, err)
	assert.Equal(t, "Config configures b.", docs["example.com/cfg/b.Config"])
	assert.Equal(t, "Timeout bounds a call | in seconds.",
		docs["example.com/cfg/a.Config.Timeout"])

	_, err = schemagen.ParseGoSchema(t.Context(),
		memoryCodebase(t, map[string]string{"schema/x.go": "package"}), "schema")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// pythonRunner is a CommandRunner mock whose `uv run python -c` calls answer from
// out by the class or enum name in the script; a name without an answer fails.
func pythonRunner(t *testing.T, out map[string]string) *mocks.MockCommandRunner {
	t.Helper()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	r.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "uv", "run", "python", "-c",
		gomock.Any()).DoAndReturn(
		func(_ context.Context, _, _ string, args ...string) interfaces.CmdResult {
			for name, body := range out {
				if strings.Contains(args[len(args)-1], "import "+name) {
					return interfaces.CmdResult{Stdout: []byte(body)}
				}
			}
			return interfaces.CmdResult{
				ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "ImportError"),
			}
		}).AnyTimes()
	return r
}

// TestFoundationSchemagen_ConfigDocs tests the config reference generator and audit.
//
// Why this test is important:
//   - The committed reference is drift-checked, so its rendering is the contract; a
//     composite default spanning lines or a `|` in a doc would break the table; the
//     audit is the gate that keeps every field documented in both languages.
//
// What it tests:
//   - The generator renders the preamble, the Selectable Kinds table and one table per
//     section (Go then Python) with defaults on one row and pipes escaped, and the
//     output is in sync with itself under gen.Check; a Python service that cannot be
//     introspected is left out of the render.
//   - AuditContract flags a Go field with no doc comment and a Python field with no
//     description, and fails when a present service cannot be introspected.
func TestFoundationSchemagen_ConfigDocs(t *testing.T) {
	t.Parallel()
	files := schemaSources()
	files["svc/app/settings.py"] = "class Settings: ...\n"
	files["broken/app/settings.py"] = "class Broken: ...\n"
	runner := pythonRunner(t, map[string]string{
		"Settings": `{"properties": {"port": {"type": "integer", "default": 80,` +
			` "description": "Port served."}, "mode": {}}}`,
		"Mode": `["fast", "safe"]`,
	})
	cfg := schemagen.DocsConfig{
		Path:      "docs/config.md",
		Preamble:  "# Config\n\n",
		SchemaDir: "schema",
		Python: []schemagen.PythonSettingsRef{
			{Dir: "svc", Module: "app.settings", Class: "Settings"},
			{Dir: "gone", Module: "x", Class: "Gone"},
		},
		GoKinds: []schemagen.GoKindRef{{Label: "Store", Key: "store.kind", PackageDir: "store"}},
		PythonKinds: []schemagen.PythonKindRef{
			{Label: "Mode", Key: "mode.kind", Dir: "svc", Module: "app.kinds", Enum: "Mode"},
		},
	}
	deps := schemagen.DocsDeps{Runner: runner, Root: "/repo"}
	g := schemagen.NewConfigDocs(cfg, deps)
	out, err := g.Render(t.Context(), memoryCodebase(t, files))
	require.NoError(t, err)
	doc := string(out["docs/config.md"])
	assert.True(t, strings.HasPrefix(doc, "# Config\n\n## Selectable Kinds\n"), doc)
	assert.Contains(t, doc, "| Mode | `mode.kind` | `fast`, `safe` | python |\n")
	assert.Contains(t, doc, "| Store | `store.kind` | `memory`, `postgres` | go |\n")
	assert.Contains(t, doc, "## a.Config (go)\n")
	assert.Contains(t, doc, "| `Timeout` | `int` | `timeout` | `5` | "+
		"Timeout bounds a call \\| in seconds. |\n")
	assert.Contains(t, doc, "| `Tags` | `map[string]string` | `tags` | "+
		"`map[string]string{\"a\": \"b\"}` | Tags are composite. |\n")
	assert.Contains(t, doc, "## Settings (python)\n")
	assert.Contains(t, doc, "| `mode` | `object` | `mode` |  |  |\n")
	assert.Less(t, strings.Index(doc, "## b.Config"), strings.Index(doc, "## Settings"))

	files["docs/config.md"] = doc
	d, err := gen.Check(t.Context(), memoryCodebase(t, files), g)
	require.NoError(t, err)
	assert.True(t, d.InSync, d.Diff)

	violations, err := schemagen.AuditContract(t.Context(), memoryCodebase(t, files), cfg, deps)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Config.Name: missing doc comment",
		"Settings.mode: missing Field(description=…)",
	}, violations)

	cfg.Python = append(cfg.Python,
		schemagen.PythonSettingsRef{Dir: "broken", Module: "app.settings", Class: "Broken"})
	_, err = schemagen.AuditContract(t.Context(), memoryCodebase(t, files), cfg, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Broken")
}
