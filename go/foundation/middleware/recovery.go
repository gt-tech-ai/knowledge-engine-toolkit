package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Recovery returns middleware that catches panics and returns 500 Internal Server Error.
func Recovery(logger interfaces.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					respondToPanic(logger, w, r, rec)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// respondToPanic logs the recovered panic value and its stack under the request's
// context and writes a 500.
func respondToPanic(
	logger interfaces.Logger,
	w http.ResponseWriter,
	r *http.Request,
	rec any,
) {
	logger.WithContext(r.Context()).Error(
		"panic recovered",
		"panic", rec,
		"stack", string(debug.Stack()),
		"method", r.Method,
		"path", r.URL.Path,
	)
	http.Error(
		w,
		http.StatusText(http.StatusInternalServerError),
		http.StatusInternalServerError,
	)
}
