// Package metrics provides a builder for the Prometheus metrics implementation.
//
// Use New() or NewFromConfig() to create a metrics instance. When metrics are
// disabled via configuration, a silent no-op implementation is returned.
//
// Example:
//
//	m, err := metrics.New(metrics.KindPrometheus, metrics.WithPath("/metrics"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	requests := m.Counter("http_requests_total", "Total HTTP requests", "method")
//	requests.Inc("GET")
package metrics

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/noop"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which metrics implementation to use.
type Kind int

const (
	// KindPrometheus uses Prometheus metrics with full collector support.
	// Suitable for production with Grafana dashboards and alerting.
	KindPrometheus Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindPrometheus:
		return "prometheus"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates a Metrics of the specified kind with optional functional options.
// Returns an error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.Metrics, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a Metrics from a Config struct.
// Returns an error if the kind is unknown.
// When Enabled is false, returns a no-op implementation regardless of the
// configured Kind.
func NewFromConfig(cfg Config) (interfaces.Metrics, error) {
	if !cfg.Enabled {
		return noop.New(), nil
	}

	switch cfg.Kind {
	case KindPrometheus:
		return prom.New(), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown metrics kind: %v", cfg.Kind),
		)
	}
}
