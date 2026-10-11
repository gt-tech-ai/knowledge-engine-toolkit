package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// requestIDKey is the unexported context key under which the request ID is
// stored, kept private to prevent collisions with other packages' context values.
type requestIDKey struct{}

// requestIDHeader is the HTTP header carrying the request correlation ID on both
// inbound requests and outbound responses.
const requestIDHeader = "X-Request-ID"

// RequestID returns middleware that ensures every request has an X-Request-ID header.
// If the incoming request doesn't have one, a new UUID is generated.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if id == "" {
				id = uuid.NewString()
			}

			// Set on response
			w.Header().Set(requestIDHeader, id)

			// Store in context
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetRequestID extracts the request ID from context.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}
