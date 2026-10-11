// Package middleware provides stdlib net/http middleware for composing
// request processing pipelines without external web frameworks.
package middleware

import "net/http"

// Middleware is a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain composes multiple middleware into a single middleware.
// Middleware are applied in order: Chain(a, b, c)(handler) = a(b(c(handler))).
func Chain(middlewares ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}
