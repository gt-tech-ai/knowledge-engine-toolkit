// Package middleware provides composable stdlib net/http middleware for Go services,
// for building request processing pipelines without external web frameworks.
//
// Each middleware follows the func(http.Handler) http.Handler pattern and can be chained
// using Chain(). Configuration structs expose a Default*Config() constructor with
// sensible development defaults.
package middleware
