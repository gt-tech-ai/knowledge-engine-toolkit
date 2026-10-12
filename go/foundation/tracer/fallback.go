package tracer

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
)

// NewFromTracing builds service's tracer from the tracing config section: the OTel
// exporter when tracing is enabled, the noop tracer when it is not. A build failure (an
// unreachable or malformed collector address) falls back to the noop tracer, so a
// missing collector never blocks boot; warn, when non-nil, reports the fallback. It
// runs before the service's logger exists, so warn is a printf-style hook.
func NewFromTracing(
	ctx context.Context,
	service string,
	t infra.TracingConfig,
	warn func(format string, args ...any),
) interfaces.Tracer {
	cfg := DefaultConfig(service)
	cfg.Endpoint = t.Endpoint
	cfg.SampleRate = t.SampleRate
	if !t.Enabled {
		cfg.Kind = KindNoop
	}
	tr, err := NewFromConfig(ctx, cfg)
	if err == nil {
		return tr
	}
	if warn != nil {
		warn("%s: OTel tracer unavailable (endpoint=%s), using noop: %v",
			service, t.Endpoint, err)
	}
	return &nooptracer.Tracer{}
}
