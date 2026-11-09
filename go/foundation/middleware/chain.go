package middleware

import (
	"net/http"
	"slices"
)

// Middleware is a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain composes multiple middleware into a single middleware.
// Middleware are applied in order: Chain(a, b, c)(handler) = a(b(c(handler))).
func Chain(middlewares ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for _, middleware := range slices.Backward(middlewares) {
			next = middleware(next)
		}

		return next
	}
}
