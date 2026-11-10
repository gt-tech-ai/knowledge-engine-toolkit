package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Recovery returns middleware that catches panics and returns 500 Internal Server Error.
func Recovery(logger interfaces.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					respondToPanic(logger, writer, request, rec)
				}
			}()

			next.ServeHTTP(writer, request)
		})
	}
}

// respondToPanic logs the recovered panic value and its stack under the request's
// context and writes a 500.
func respondToPanic(
	logger interfaces.Logger,
	writer http.ResponseWriter,
	request *http.Request,
	rec any,
) {
	logger.WithContext(request.Context()).Error(
		"panic recovered",
		"panic", rec,
		"stack", string(debug.Stack()),
		"method", request.Method,
		"path", request.URL.Path,
	)
	http.Error(
		writer,
		http.StatusText(http.StatusInternalServerError),
		http.StatusInternalServerError,
	)
}
