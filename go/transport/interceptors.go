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
func Interceptors[I any](
	name string,
	d decorate.Deps,
	native func(decorate.Layer) ([]I, bool),
	adapt func(unary, stream decorate.OpMiddleware) I,
	authn []I,
) []I {
	tier := Tier()
	streamDeps := d
	streamDeps.Timeout = 0
	var (
		out           []I
		unary, stream []decorate.OpMiddleware
	)
	flush := func() {
		if len(unary)+len(stream) > 0 {
			out = append(out, adapt(decorate.Chain(unary...), decorate.Chain(stream...)))
		}
		unary, stream = nil, nil
	}
	for _, layer := range tier.Order {
		if layer == decorate.LayerAuth {
			flush()
			out = append(out, authn...)
		}
		if ics, ok := native(layer); ok {
			flush()
			out = append(out, ics...)
			continue
		}
		if mw := tier.Middleware(layer, name, d); mw != nil {
			unary = append(unary, mw)
		}
		if mw := tier.Middleware(layer, name, streamDeps); mw != nil {
			stream = append(stream, mw)
		}
	}
	flush()
	return out
}
