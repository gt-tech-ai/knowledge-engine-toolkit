// Package decorators wraps a connector ConnectorSource with the shared client
// observability stack (metrics/tracing/logging). It composes the same
// go/clients/decorators stack the storage and messaging clients use (DRY,
// one stack for every client); resilience (retry/circuit-breaker) is
// deliberately omitted — the sync engine owns
// retriable-vs-terminal policy, not the rare "test connection" probe.
package decorators

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Deps carries the observability collaborators wrapped around a
// ConnectorSource; a nil field disables that layer.
type Deps struct {
	// Logger records each failed op with its name + error; nil disables the
	// failure log.
	Logger interfaces.Logger

	// Metrics records per-op counts/errors/latency; nil disables metrics.
	Metrics interfaces.Metrics

	// Tracer emits a span per op; nil disables tracing.
	Tracer oteltrace.Tracer
}

// Wrap composes the shared client observability stack around a
// ConnectorSource, labelled by name.
func Wrap(
	base interfaces.ConnectorSource,
	name string,
	deps Deps,
) interfaces.ConnectorSource {
	stack := clientstack.New(name).
		WithTracer(deps.Tracer).
		WithMetrics(deps.Metrics).
		WithLogger(deps.Logger)
	return &decorator{base: base, stack: stack}
}

// decorator runs each ConnectorSource op through the shared observability
// stack.
type decorator struct {
	// base is the wrapped ConnectorSource whose ops are observed.
	base interfaces.ConnectorSource
	// stack is the shared observability stack (tracer, metrics, logger).
	stack *clientstack.Stack
}

// Ensure decorator satisfies the ConnectorSource contract.
var _ interfaces.ConnectorSource = (*decorator)(nil)

// TestConnection runs the probe through the observability stack
// (op="test_connection").
func (d *decorator) TestConnection(ctx context.Context) (int, error) {
	return clientstack.Run(ctx, d.stack, "test_connection", clientstack.RunOpts{},
		func(c context.Context) (int, error) {
			return d.base.TestConnection(c)
		})
}

// listPageResult bundles a page + its resume token so the single-value
// observability Run carries both.
type listPageResult struct {
	// next is the resume/continuation token for the following page ("" when
	// done).
	next string

	// objects is the batch of storage objects in this page.
	objects []interfaces.StorageObject
}

// ListPage runs one streaming list page through the observability stack
// (op="list_page"). Each page a large-bucket crawl fetches is a distinct
// span/metric, so a sync's listing latency is observable.
func (d *decorator) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	res, err := clientstack.Run(ctx, d.stack, "list_page", clientstack.RunOpts{},
		func(c context.Context) (listPageResult, error) {
			objs, next, listErr := d.base.ListPage(c, continuationToken, limit)
			return listPageResult{objects: objs, next: next}, listErr
		})
	return res.objects, res.next, err
}
