package unit_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/pipelines"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workers"
)

// TestPipelinesDefaults_MatchConsts tests the pipeline config's multipart-upload
// defaults.
//
// Why this test is important:
//   - The multipart threshold/part size drive how uploads are chunked; a wrong
//     default would change upload behavior the moment the provider wires it in.
//
// What it tests:
//   - MultipartThresholdBytes 20 MiB and MultipartPartSizeBytes 16 MiB, and the
//     defaults validate.
func TestPipelinesDefaults_MatchConsts(t *testing.T) {
	t.Parallel()

	c := pipelines.DefaultConfig()
	assert.Equal(t, int64(20<<20), c.Upload.MultipartThresholdBytes)
	assert.Equal(t, int64(16<<20), c.Upload.MultipartPartSizeBytes)
	require.NoError(t, c.Validate())
}

// TestMaxMultipartParts_StaysConst tests that maxMultipartParts (S3's 10 000-part
// protocol ceiling) is NOT exposed as a pipelines config key.
//
// Why this test is important:
//   - Risks call it out explicitly: maxMultipartParts is a protocol
//     constant, not a deployer knob. Exposing it as config would invite a value
//     that breaks the S3 contract. This is the scope-creep guard — it fails if
//     anyone adds a multipart-parts field to the pipeline config surface.
//
// What it tests:
//   - No field of pipelines.Config (or its nested UploadConfig) has a name or
//     mapstructure tag referencing multipart parts.
func TestMaxMultipartParts_StaysConst(t *testing.T) {
	t.Parallel()

	if hasMultipartPartsField(reflect.TypeOf(pipelines.Config{})) {
		t.Fatal(
			"maxMultipartParts must stay a const, not a pipelines config key (scope guard)",
		)
	}
}

// hasMultipartPartsField reports whether t (recursively) has a field named or
// tagged with "multipart_parts" / "MaxMultipartParts".
func hasMultipartPartsField(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		name := strings.ToLower(f.Name)
		tag := strings.ToLower(f.Tag.Get("mapstructure"))
		// Match the part-COUNT field specifically (MaxMultipartParts / the
		// multipart_parts key), not the part-SIZE field (MultipartPartSizeBytes).
		if strings.Contains(name, "maxmultipartparts") ||
			tag == "multipart_parts" || tag == "max_multipart_parts" {
			return true
		}
		if f.Type.Kind() == reflect.Struct && hasMultipartPartsField(f.Type) {
			return true
		}
	}
	return false
}

// TestWorkersConfig_ValidatesSweepTimings tests that the generalized workers
// schema keeps the sweep-timing cross-field invariant.
//
// Why this test is important:
//   - This invariant is a sweeping worker's hard-won safety rule:
//     the reaper/orphan TTLs must outlive an in-flight multipart upload's presign
//     window, or a sweep deletes a live upload. Generalizing the config must NOT
//     lose it.
//
// What it tests:
//   - A ReaperTTL at or below the multipart expiry is rejected; a ReaperTTL/
//     OrphanGrace above it passes; a zero expiry skips the invariant.
func TestWorkersConfig_ValidatesSweepTimings(t *testing.T) {
	t.Parallel()

	expiry := 60 * time.Minute
	good := workers.Config{
		Port:        8085,
		ReaperTTL:   90 * time.Minute,
		OrphanGrace: 2 * time.Hour,
	}
	require.NoError(t, good.Validate(expiry))

	bad := good
	bad.ReaperTTL = 30 * time.Minute // <= expiry
	require.Error(
		t,
		bad.Validate(expiry),
		"reaper_ttl <= multipart expiry must be rejected",
	)

	// A zero multipart expiry skips the cross-field invariant.
	require.NoError(t, bad.Validate(0))
}

// TestWorkersDefaults_AreNeutralAndValid tests the shared workers tier's defaults.
//
// Why this test is important:
//   - Every worker composes its overlay over these defaults; a default that named one
//     worker (its service name or port) would leak into every other, and defaults that
//     broke the sweep-timing invariant would reject an empty overlay.
//
// What it tests:
//   - No service name or port is set (the worker sets its own); Env is "dev"; every
//     cadence, TTL and batching knob carries its documented conservative value; and the
//     defaults validate against a 60m multipart presign expiry.
func TestWorkersDefaults_AreNeutralAndValid(t *testing.T) {
	t.Parallel()

	assert.Equal(t, workers.Config{
		Env:               "dev",
		OTelEndpoint:      "localhost:4317",
		RelayInterval:     10 * time.Second,
		CleanupInterval:   24 * time.Hour,
		ReaperInterval:    5 * time.Minute,
		ReaperTTL:         90 * time.Minute,
		RetentionInterval: time.Hour,
		RetentionTTL:      7 * 24 * time.Hour,
		OrphanGrace:       2 * time.Hour,
		BatchSize:         100,
		FanOutWorkers:     8,
	}, workers.DefaultConfig(), "no service name or port; conservative cadences, TTLs "+
		"and batching")
	require.NoError(t, workers.DefaultConfig().Validate(60*time.Minute),
		"the defaults must satisfy the sweep-timing invariant")
}

// TestWorkersConfig_OverlayIsParsed tests that a `workers` overlay is decoded onto the
// shared workers config through the real Viper loader, changing the parsed common config.
//
// Why this test is important:
//   - Every common worker knob must be overlay-overridable. The mapstructure
//     tags are the contract that makes that possible; a wrong/missing tag would silently
//     ignore an overlay value and pin the default in every deployment. This drives the
//     exact production decode path (base.yaml -> UnmarshalKey) over the shared tier.
//
// What it tests:
//   - A `workers:` block overriding port, fan_out_workers, and relay_interval yields
//     those three values on the parsed config, while an unspecified field (batch_size)
//     keeps the seeded default — proving the overlay changes only what it names.
func TestWorkersConfig_OverlayIsParsed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
workers:
  port: 9000
  fan_out_workers: 16
  relay_interval: 30s
`)
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	cfg := workers.DefaultConfig()
	require.NoError(t, loader.UnmarshalKey("workers", &cfg))

	assert.Equal(t, 9000, cfg.Port, "workers.port overlay must be parsed")
	assert.Equal(
		t,
		16,
		cfg.FanOutWorkers,
		"workers.fan_out_workers overlay must be parsed",
	)
	assert.Equal(
		t,
		30*time.Second,
		cfg.RelayInterval,
		"workers.relay_interval overlay must be parsed",
	)
	assert.Equal(t, 100, cfg.BatchSize, "an unspecified field keeps the seeded default")
}
