package transport

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"

// Interceptors lays out a server framework's interceptor chain (I is its interceptor
// type) as the controller tier for the controller named name over d, outermost first,
// so an RPC server is decorated exactly like a Base handler:
//
//   - A layer the framework implements natively — a trace extracted from the request
//     headers, metrics labelled by status code, a status-aware access log — comes from
//     native, which reports whether it handles the layer and returns its interceptors
//     (none when the collaborator is absent).
//   - Every other run of consecutive layers becomes one interceptor, built by adapt
//     from the run's middleware twice over: once for a unary call and once for a
//     stream, which has no timeout so a long-lived stream outlives any call budget.
//     A run whose layers all lack their collaborators adds nothing.
//   - authn, the framework's interceptors that authenticate the caller, run just
//     before the auth layer, so it authorizes an authenticated principal.
//
// d must not be nil.
func Interceptors[I any](
	name string,
	deps *decorate.Deps,
	native func(decorate.Layer) ([]I, bool),
	adapt func(unary, stream decorate.OpMiddleware) I,
	authn []I,
) []I {
	tier := Tier()
	streamDeps := *deps

	streamDeps.Timeout = 0

	currentInterceptorRun := &interceptorRun[I]{adapt: adapt}

	for _, layer := range tier.Order {
		if layer == decorate.LayerAuth {
			currentInterceptorRun.place(authn)
		}

		if ics, ok := native(layer); ok {
			currentInterceptorRun.place(ics)

			continue
		}

		currentInterceptorRun.add(
			tier.Middleware(layer, name, deps),
			tier.Middleware(layer, name, &streamDeps),
		)
	}

	currentInterceptorRun.flush()

	return currentInterceptorRun.out
}

// interceptorRun lays middleware out as interceptors, merging each run of adjacent
// layers into one.
type interceptorRun[I any] struct {
	// adapt shapes a run's unary and stream chains into one interceptor.
	adapt func(unary, stream decorate.OpMiddleware) I

	// out holds the interceptors laid out so far.
	out []I

	// unary holds the current run's unary layers.
	unary []decorate.OpMiddleware

	// stream holds the current run's stream layers.
	stream []decorate.OpMiddleware
}

// add extends the current run with a layer's unary and stream middleware, either of
// which may be absent.
func (b *interceptorRun[I]) add(unary, stream decorate.OpMiddleware) {
	if unary != nil {
		b.unary = append(b.unary, unary)
	}

	if stream != nil {
		b.stream = append(b.stream, stream)
	}
}

// flush closes the current run into one interceptor.
func (b *interceptorRun[I]) flush() {
	if len(b.unary)+len(b.stream) > 0 {
		b.out = append(
			b.out,
			b.adapt(decorate.Chain(b.unary...), decorate.Chain(b.stream...)),
		)
	}

	b.unary, b.stream = nil, nil
}

// place closes the current run and appends ready-made interceptors after it.
func (b *interceptorRun[I]) place(ics []I) {
	b.flush()

	b.out = append(b.out, ics...)
}
