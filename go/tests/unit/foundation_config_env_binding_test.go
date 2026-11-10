package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	viperloader "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/viper"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// baseWithDB writes a minimal base.yaml with a database + storage section into a
// temp dir and returns the dir, for the env-binding tests. database.max_connections,
// database.user and every storage.s3 key but bucket are deliberately absent, so only a
// derived env binding can surface them through UnmarshalKey.
func baseWithDB(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
database:
  host: "localhost"
  port: 5432
storage:
  s3:
    bucket: "from-yaml"
`)

	return dir
}

// bindingSchemaStorage is the Storage element of bindingSchema.
type bindingSchemaStorage struct {
	// S3 is the object-store section (storage.s3.*).
	S3 infra.S3Config `mapstructure:"s3"`
}

// bindingSchemaMessaging is the Messaging element of bindingSchema.
type bindingSchemaMessaging struct {
	// SQS is the queue section (messaging.sqs.*).
	SQS infra.SQSConfig `mapstructure:"sqs"`
}

// bindingSchema is a consumer root config: the loader derives an env binding for
// every leaf, under the consumer's prefix, from these mapstructure/envalias tags.
type bindingSchema struct {
	// Messaging groups the queue backend under messaging.sqs.
	Messaging bindingSchemaMessaging `mapstructure:"messaging"`
	// Auth is the auth section (auth.*).
	Auth infra.AuthConfig `mapstructure:"auth"`
	// Tracing is the tracing section (tracing.*).
	Tracing infra.TracingConfig `mapstructure:"tracing"`
	// Storage groups the object store under storage.s3.
	Storage bindingSchemaStorage `mapstructure:"storage"`
	// Database is the database section (database.*).
	Database infra.DatabaseConfig `mapstructure:"database"`
	// Redis is the cache section (redis.*).
	Redis infra.RedisConfig `mapstructure:"redis"`
}

// loadWithSchema builds the loader over dir with bindingSchema and the MYAPP prefix,
// the way a consumer builds it, plus any extra options.
func loadWithSchema(
	t *testing.T, dir string, extra ...options.Option[config.Config],
) interfaces.ConfigLoader {
	t.Helper()

	opts := []options.Option[config.Config]{
		config.WithBaseDir(dir),
		config.WithEnvironment(""),
		config.WithEnvPrefix(textMyapp),
		config.WithSchema(new(bindingSchema)),
	}
	cfg, err := config.New(config.KindViper, append(opts, extra...)...)
	require.NoError(t, err)

	return cfg
}

// TestConfig_CanonicalPrefixedPath_BindsKeyAbsentFromYAML tests that the canonical
// <PREFIX>_<PATH> env var of a schema leaf reaches UnmarshalKey even when the key is
// absent from every YAML layer.
//
// Why this test is important:
//   - UnmarshalKey decodes only the keys Viper knows (YAML keys plus bound env keys); an
//     env-only value such as a secret never in the YAML is silently dropped unless the
//     schema derived a binding for it.
//
// What it tests:
//   - MYAPP_DATABASE_MAX_CONNECTIONS and MYAPP_MESSAGING_SQS_REGION, whose keys are in no
//     YAML file, override their fields when the sections are unmarshaled.
func TestConfig_CanonicalPrefixedPath_BindsKeyAbsentFromYAML(t *testing.T) {
	dir := baseWithDB(t)
	writeFile(t, filepath.Join(dir, textBaseYaml), `
database:
  host: "localhost"
messaging:
  sqs:
    endpoint: http://localhost:9324
`)
	t.Setenv("MYAPP_DATABASE_MAX_CONNECTIONS", "77")
	t.Setenv("MYAPP_MESSAGING_SQS_REGION", textEuWest1)

	cfg := loadWithSchema(t, dir)

	var db infra.DatabaseConfig

	require.NoError(t, cfg.UnmarshalKey(textDatabase, &db))
	assert.Equal(t, wantMaxConnections, db.MaxConnections,
		"MYAPP_DATABASE_MAX_CONNECTIONS must bind database.max_connections")

	var sqs infra.SQSConfig

	require.NoError(t, cfg.UnmarshalKey("messaging.sqs", &sqs))
	assert.Equal(
		t,
		textEuWest1,
		sqs.Region,
		"MYAPP_MESSAGING_SQS_REGION must bind messaging.sqs.region",
	)
}

// TestConfig_EnvAlias_Resolves tests that the flat aliases from the schema's envalias
// tags (DB_HOST, S3_BUCKET, OTEL_EXPORTER_OTLP_ENDPOINT) override their mapped fields.
//
// Why this test is important:
//   - Deployments set flat names such as DB_HOST or the OpenTelemetry-standard
//     OTEL_EXPORTER_OTLP_ENDPOINT; a dropped alias silently falls back to the YAML
//     default (wrong DB host, bucket or collector).
//
// What it tests:
//   - DB_HOST overrides database.host, S3_BUCKET overrides storage.s3.bucket and
//     OTEL_EXPORTER_OTLP_ENDPOINT overrides tracing.endpoint (absent from the YAML)
//     through UnmarshalKey.
func TestConfig_EnvAlias_Resolves(t *testing.T) {
	dir := baseWithDB(t)
	writeFile(t, filepath.Join(dir, textBaseYaml), `
database:
  host: "localhost"
storage:
  s3:
    bucket: "from-yaml"
tracing:
  enabled: true
`)
	t.Setenv(textDBHost, textDBInternal)
	t.Setenv("S3_BUCKET", "prod-bucket")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317")

	cfg := loadWithSchema(t, dir)

	var db infra.DatabaseConfig

	require.NoError(t, cfg.UnmarshalKey(textDatabase, &db))
	assert.Equal(t, textDBInternal, db.Host, "DB_HOST must override database.host")

	var s3 infra.S3Config

	require.NoError(t, cfg.UnmarshalKey("storage.s3", &s3))
	assert.Equal(t, "prod-bucket", s3.Bucket, "S3_BUCKET must override storage.s3.bucket")

	var tracing infra.TracingConfig

	require.NoError(t, cfg.UnmarshalKey(textTracing, &tracing))
	assert.Equal(t, "collector:4317", tracing.Endpoint,
		"OTEL_EXPORTER_OTLP_ENDPOINT must override tracing.endpoint")
}

// TestConfig_EmptyPrefix_BindsBarePath tests that with no env prefix (the default) a
// schema leaf binds its bare <PATH> name.
//
// Why this test is important:
//   - The default prefix is empty; if the empty-prefix branch produced "_DATABASE_…" or
//     dropped the name, a consumer on the default would lose every derived override.
//
// What it tests:
//   - With a schema and no prefix, DATABASE_MAX_CONNECTIONS (key absent from the YAML)
//     reaches UnmarshalKey.
func TestConfig_EmptyPrefix_BindsBarePath(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("DATABASE_MAX_CONNECTIONS", "33")

	cfg, err := config.New(config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment(""),
		config.WithSchema(new(bindingSchema)),
	)
	require.NoError(t, err)

	var databaseConfig infra.DatabaseConfig

	require.NoError(t, cfg.UnmarshalKey(textDatabase, &databaseConfig))
	assert.Equal(
		t,
		wantMaxConnectionsPath,
		databaseConfig.MaxConnections,
		"DATABASE_MAX_CONNECTIONS must bind with no prefix",
	)
}

// TestConfig_CustomPrefixOwnsDerivedEnvBindings tests that the consumer's env prefix, and
// only it, names the env vars derived from the schema.
//
// Why this test is important:
//   - A consumer that sets its own prefix must control which env vars override its
//     config; an env var under someone else's prefix silently changing a value is a
//     config leak.
//
// What it tests:
//   - With prefix MYAPP and a schema, MYAPP_STORAGE_S3_PUBLIC_ENDPOINT (key absent from
//     the YAML) binds storage.s3.public_endpoint.
//   - OTHERAPP_STORAGE_S3_BUCKET does not override the YAML bucket, and
//     OTHERAPP_STORAGE_S3_REGION does not bind the absent region key.
func TestConfig_CustomPrefixOwnsDerivedEnvBindings(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("MYAPP_STORAGE_S3_PUBLIC_ENDPOINT", "from-myapp-env")
	t.Setenv("OTHERAPP_STORAGE_S3_BUCKET", "from-other-env")
	t.Setenv("OTHERAPP_STORAGE_S3_REGION", "from-other-env")

	cfg := loadWithSchema(t, dir)

	var s3Config infra.S3Config

	require.NoError(t, cfg.UnmarshalKey("storage.s3", &s3Config))
	assert.Equal(
		t,
		"from-myapp-env",
		s3Config.PublicEndpoint,
		"the consumer's prefix must name the derived env var",
	)
	assert.Equal(
		t,
		"from-yaml",
		s3Config.Bucket,
		"an env var under a different prefix must not override",
	)
	assert.Empty(t, s3Config.Region, "an env var under a different prefix must not bind")
}

// TestConfig_ExtraEnvReplacesSchemaBinding tests that an ExtraEnv entry for a schema leaf
// replaces the binding the schema derived for it, as the option documents.
//
// Why this test is important:
//   - ExtraEnv is how a consumer repoints a field at its own variable (e.g. PGHOST
//     instead of the DB_HOST alias). If the schema's alias still won, a stray DB_HOST in
//     the pod would silently connect the service to the wrong database.
//
// What it tests:
//   - With ExtraEnv{database.host: [PGHOST]} and both DB_HOST and PGHOST set, PGHOST
//     wins.
//   - With only DB_HOST set, the replaced alias no longer binds (the YAML value stands).
func TestConfig_ExtraEnvReplacesSchemaBinding(t *testing.T) {
	dir := baseWithDB(t)
	repoint := config.WithExtraEnv(map[string][]string{"database.host": {"PGHOST"}})

	t.Setenv(textDBHost, "from-alias")
	t.Setenv("PGHOST", "from-extra")

	var databaseConfig infra.DatabaseConfig

	require.NoError(
		t,
		loadWithSchema(t, dir, repoint).UnmarshalKey(textDatabase, &databaseConfig),
	)
	assert.Equal(
		t,
		"from-extra",
		databaseConfig.Host,
		"the ExtraEnv name must win over the schema alias",
	)

	t.Setenv("PGHOST", "")
	require.NoError(
		t,
		loadWithSchema(t, dir, repoint).UnmarshalKey(textDatabase, &databaseConfig),
	)
	assert.Equal(
		t,
		"localhost",
		databaseConfig.Host,
		"the replaced schema alias must not bind",
	)
}

// squashRoot is a consumer root that flattens a section with ",squash" and uses tag
// options and an untagged field, the way mapstructure lets a consumer write its structs.
type squashRoot struct {
	// Label carries a tag option after the name.
	Label string `mapstructure:"label,omitempty"`
	// Skipped is excluded from decoding.
	Skipped string `mapstructure:"-"`

	// DatabaseConfig is flattened into the root: its keys are host, port, ….
	DatabaseConfig infra.DatabaseConfig `mapstructure:"database_config,squash"`

	// Replicas has no tag, so mapstructure matches it by field name.
	Replicas int
}

// TestConfig_SchemaTagForms_Bind tests that the derived bindings follow mapstructure's
// own reading of the tags: the name before the comma, ",squash" flattening, and the
// field-name fallback for an untagged field.
//
// Why this test is important:
//   - WithSchema takes consumer structs; binding the raw tag (",squash.host",
//     "label,omitempty") or skipping untagged fields silently ignores every env-only
//     override of those fields.
//
// What it tests:
//   - For keys absent from the YAML: the squashed section's DB_HOST alias and MYAPP_PORT
//     bind host and port at the root, MYAPP_LABEL binds the "label,omitempty" field, and
//     MYAPP_REPLICAS binds the untagged Replicas field; Unmarshal sees them all.
//   - A "-" field gets no binding.
func TestConfig_SchemaTagForms_Bind(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), "user: app\n")
	t.Setenv(textDBHost, textDBInternal)
	t.Setenv("MYAPP_PORT", "6543")
	t.Setenv("MYAPP_LABEL", "blue")
	t.Setenv("MYAPP_REPLICAS", "3")
	t.Setenv("MYAPP_SKIPPED", "bound")

	cfg, err := config.New(config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment(""),
		config.WithEnvPrefix(textMyapp),
		config.WithSchema(stdx.Zero[squashRoot]()),
	)
	require.NoError(t, err)

	var root squashRoot

	require.NoError(t, cfg.Unmarshal(&root))
	assert.Equal(
		t,
		textDBInternal,
		root.DatabaseConfig.Host,
		"the squashed DB_HOST alias must bind host",
	)
	assert.Equal(
		t,
		wantPortBind,
		root.DatabaseConfig.Port,
		"MYAPP_PORT must bind the squashed port",
	)
	assert.Equal(
		t,
		textApp,
		root.DatabaseConfig.User,
		"YAML values of the squashed section still decode",
	)
	assert.Equal(t, "blue", root.Label, "the tag name before the comma names the key")
	assert.Equal(
		t,
		wantReplicas,
		root.Replicas,
		"an untagged field binds by its field name",
	)

	loader, ok := cfg.(*viperloader.Loader)
	require.True(t, ok)
	assert.NotContains(
		t,
		loader.Viper().AllKeys(),
		"skipped",
		"a \"-\" field must not be bound",
	)
}

// TestConfig_InvalidSchemaRejected tests that a Schema that is not a struct (or a pointer
// to one) fails Load instead of silently deriving nothing.
//
// Why this test is important:
//   - A consumer passing a map or a scalar would otherwise lose every derived env
//     override with no error pointing at the cause.
//
// What it tests:
//   - A map, a scalar and a pointer to a pointer each make config.New return
//     CodeInvalidInput.
func TestConfig_InvalidSchemaRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root := new(bindingSchema)

	for name, schema := range map[string]any{
		"map":                map[string]any{textDatabase: map[string]any{}},
		textScalar:           textScalarRejected,
		"pointer to pointer": &root,
	} {
		_, err := config.New(
			config.KindViper,
			config.WithBaseDir(dir),
			config.WithSchema(schema),
		)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%s: %v", name, err)
	}
}

// TestConfig_ServiceTokens_FlatEnvPopulatesMap tests that a flat
// "caller=token;caller=token" env var (SERVICE_AUTH_TOKENS) populates the
// auth.service_tokens map[string]string through UnmarshalKey, preserving a
// caller's comma-separated {current,previous} rotation pair.
//
// Why this test is important:
//   - A deploy delivers the per-caller service tokens a
//     server accepts as a single Kubernetes secretKeyRef env var — a scalar, the
//     only shape a secretKeyRef can carry. Without a string→map decode hook the
//     map field stays nil, so in enforce mode every internal caller is rejected: a
//     fleet-wide internal-RPC outage. The hook is the foundation the server-side
//     enforcement rests on.
//   - Entries are ";"-separated, not ","-separated, so a caller's dual-token
//     rotation value ("cur,prev") survives — a "," delimiter would truncate the
//     window and lock out the caller mid-rotation.
//
// What it tests:
//   - SERVICE_AUTH_TOKENS="reports=rcur,rprev;orders=otok" decodes into
//     {"reports":"rcur,rprev","orders":"otok"} on AuthConfig.ServiceTokens.
func TestConfig_ServiceTokens_FlatEnvPopulatesMap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), `
auth:
  stub: true
`)
	t.Setenv("SERVICE_AUTH_TOKENS", "reports=rcur,rprev;orders=otok")

	cfg := loadWithSchema(t, dir)

	var auth infra.AuthConfig

	require.NoError(t, cfg.UnmarshalKey("auth", &auth))
	assert.Equal(
		t,
		map[string]string{
			textReports: "rcur,rprev",
			textOrders:  textOtok,
		},
		auth.ServiceTokens,
		"SERVICE_AUTH_TOKENS must decode into the auth.service_tokens map, preserving the "+
			"rotation-pair ','",
	)
}

const (
	// textScalarRejected is the textScalar field in TestConfig_InvalidSchemaRejected.
	textScalarRejected = 42

	// wantMaxConnections is the expected max connections.
	wantMaxConnections = 77

	// wantMaxConnectionsPath is the expected max connections path.
	wantMaxConnectionsPath = 33

	// wantPortBind is the expected port bind.
	wantPortBind = 6543

	// wantReplicas is the expected replicas.
	wantReplicas = 3
)

const (
	// textDBInternal is the literal "db.internal".
	textDBInternal = "db.internal"
)
