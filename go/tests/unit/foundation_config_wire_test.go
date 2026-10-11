package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// TestProvideServerConfig tests that a config provider can load ServerConfig from YAML.
//
// Why this test is important:
//   - Config providers at the composition root feed every service its settings
//   - Must produce valid, validated config structs
//   - Must respect environment variable overrides
//
// What it tests:
//   - ConfigLoader creation with custom base dir
//   - UnmarshalKey extracts server config correctly
//   - Validation catches invalid configs (zero port)
func TestProvideServerConfig(t *testing.T) {
	t.Parallel()

	// Arrange: Create temp config directory with server config
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "base.yaml"), `
server:
  http:
    host: "0.0.0.0"
    port: 8090
    read_timeout: 30s
    read_header_timeout: 10s
    write_timeout: 30s
    idle_timeout: 120s
`)

	// Act: Simulate a provider loading one server's section
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var serverCfg infra.ServerConfig
	require.NoError(t, loader.UnmarshalKey("server.http", &serverCfg), "UnmarshalKey")

	// Assert: Config loaded correctly
	assert.Equal(t, "0.0.0.0", serverCfg.Host)
	assert.Equal(t, 8090, serverCfg.Port)
	assert.Equal(t, "30s", serverCfg.ReadTimeout.String())

	// Validate should pass
	require.NoError(t, serverCfg.Validate(), "Validate must pass for valid config")
}

// TestProvideServerConfig_ValidationFailure tests that validation catches zero port.
//
// Why this test is important:
//   - Config providers must validate configs before returning
//   - Invalid configs should cause startup failures, not runtime crashes
//
// What it tests:
//   - ServerConfig.Validate() returns error for Port == 0
func TestProvideServerConfig_ValidationFailure(t *testing.T) {
	t.Parallel()

	// Arrange: ServerConfig with zero port (invalid)
	serverCfg := infra.ServerConfig{
		Host: "0.0.0.0",
		Port: 0, // Invalid!
	}

	// Act & Assert: Validation should fail
	err := serverCfg.Validate()
	require.Error(t, err, "Validate() must return error for Port == 0")
	// Validate() now returns a coded core/errors value (ARCHITECTURE.md#error-codes), so
	// Error() carries an "INVALID_INPUT: " prefix; assert the meaningful message as a
	// substring rather than pinning the exact rendered (code-prefixed) string.
	assert.ErrorContains(t, err, "server.port is required (got 0)")
}

// TestProvideDatabaseConfig tests that a config provider can load DatabaseConfig.
//
// Why this test is important:
//   - Every service that talks to Postgres loads this section; a decode or DSN
//     mistake breaks every connection
//
// What it tests:
//   - UnmarshalKey extracts database config
//   - DSN() method produces correct connection string
func TestProvideDatabaseConfig(t *testing.T) {
	t.Parallel()

	// Arrange
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "base.yaml"), `
database:
  host: localhost
  port: 5432
  user: app
  password: dev_password
  database: knowledge_engine
  sslmode: disable
`)

	// Act
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var dbCfg infra.DatabaseConfig
	require.NoError(t, loader.UnmarshalKey("database", &dbCfg), "UnmarshalKey")

	// Assert
	assert.Equal(t, "localhost", dbCfg.Host)
	assert.Equal(t, "app", dbCfg.User)
	assert.Equal(
		t,
		"postgres://app:dev_password@localhost:5432/knowledge_engine?sslmode=disable",
		dbCfg.DSN(),
	)

	// Validate should pass
	require.NoError(t, dbCfg.Validate(), "Validate must pass for valid config")
}

// TestProvideRedisConfig tests that a config provider can load RedisConfig.
//
// Why this test is important:
//   - Services that cache in Redis load this section; a wrong Addr() misroutes every
//     cache call
//
// What it tests:
//   - UnmarshalKey extracts redis config
//   - Addr() method produces correct host:port string
func TestProvideRedisConfig(t *testing.T) {
	t.Parallel()

	// Arrange
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "base.yaml"), `
redis:
  host: localhost
  port: 6379
  password: ""
  db: 0
`)

	// Act
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var redisCfg infra.RedisConfig
	require.NoError(t, loader.UnmarshalKey("redis", &redisCfg), "UnmarshalKey")

	// Assert
	assert.Equal(t, "localhost", redisCfg.Host)
	assert.Equal(t, 6379, redisCfg.Port)
	assert.Equal(t, "localhost:6379", redisCfg.Addr())

	// Validate should pass
	require.NoError(t, redisCfg.Validate(), "Validate must pass for valid config")
}
