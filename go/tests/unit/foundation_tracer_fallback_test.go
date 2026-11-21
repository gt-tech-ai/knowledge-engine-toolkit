package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/oteltracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// TestNewFromTracing_DisabledIsNoop tests the tracing-section tracer factory.
//
// Why this test is important:
//   - Every service builds its tracer from the tracing section at boot; tracing turned
//     off must never dial a collector, and the factory must never fail boot.
//
// What it tests:
//   - A disabled section yields the noop tracer without reporting a fallback.
func TestNewFromTracing_DisabledIsNoop(t *testing.T) {
	t.Parallel()

	warned := false
	tr := tracer.NewFromTracing(t.Context(), textSvc, stdx.Zero[infra.TracingConfig](),
		func(string, ...any) { warned = true })
	require.IsType(t, &nooptracer.Tracer{}, tr)
	require.False(t, warned)
}

// TestNewFromTracing_SamplerKind tests that the tracing section's sampler kind reaches
// the OTel tracer.
//
// Why this test is important:
//   - The sampler is chosen by config, so a service selects link_aware with one line of
//     YAML. If the section's value were dropped on the way to the tracer, the setting
//     would silently do nothing; if a misspelt kind built a tracer anyway, the service
//     would sample by a rule nobody chose.
//
// What it tests:
//   - A valid kind builds the OTel tracer without reporting a fallback.
//   - An unknown kind is not swallowed: the factory reports the fallback and returns
//     the noop tracer.
func TestNewFromTracing_SamplerKind(t *testing.T) {
	t.Parallel()

	section := func(kind string) infra.TracingConfig {
		return infra.TracingConfig{
			Endpoint:   "localhost:4317",
			Sampler:    kind,
			SampleRate: linkSamplerNever,
			Enabled:    true,
		}
	}

	var warnings int

	warn := func(string, ...any) { warnings++ }

	built := tracer.NewFromTracing(
		t.Context(), textSvc, section(oteltracer.SamplerLinkAware), warn,
	)
	t.Cleanup(func() { _ = built.Shutdown(t.Context()) })
	assert.IsType(t, &oteltracer.Tracer{}, built)
	assert.Zero(t, warnings)

	fallback := tracer.NewFromTracing(t.Context(), textSvc, section("link-aware"), warn)
	assert.IsType(t, &nooptracer.Tracer{}, fallback)
	assert.Equal(t, 1, warnings)
}

// TestNewFromConfig_UnknownSampler tests the tracer factory's sampler validation.
//
// Why this test is important:
//   - A misspelt sampler kind must be a coded error at construction, so a caller that
//     does not fall back (a test, a tool) sees CodeInvalidInput, not a different rule.
//
// What it tests:
//   - An OTel tracer config with an unknown Sampler fails with CodeInvalidInput.
func TestNewFromConfig_UnknownSampler(t *testing.T) {
	t.Parallel()

	cfg := tracer.DefaultConfig(textSvc)

	cfg.Sampler = "link-aware"

	_, err := tracer.NewFromConfig(t.Context(), cfg)

	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
