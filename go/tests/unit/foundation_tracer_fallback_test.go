package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
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
	tr := tracer.NewFromTracing(t.Context(), "svc", infra.TracingConfig{Enabled: false},
		func(string, ...any) { warned = true })
	require.IsType(t, &nooptracer.Tracer{}, tr)
	require.False(t, warned)
}
