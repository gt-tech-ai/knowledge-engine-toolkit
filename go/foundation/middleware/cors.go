// Package middleware provides composable HTTP middleware for Go services.
//
// Each middleware follows the func(http.Handler) http.Handler pattern and
// can be chained using Chain(). Configuration structs expose a Default*Config()
// constructor with sensible development defaults.
package middleware

import (
	"net/http"
	"strings"
)

// CORSConfig holds Cross-Origin Resource Sharing settings.
// AllowOrigins of ["*"] permits all origins. For production, restrict
// to the specific frontend domain(s).
type CORSConfig struct {
	// MaxAge is the duration (in seconds) browsers may cache preflight results.
	MaxAge string

	// AllowOrigins is the list of permitted origins. Use ["*"] to allow all.
	AllowOrigins []string

	// AllowMethods is the list of HTTP methods included in preflight responses.
	AllowMethods []string

	// AllowHeaders is the list of request headers the client may send.
	AllowHeaders []string

	// ExposedHeaders is the list of response headers a cross-origin browser is allowed to
	// read, written as Access-Control-Expose-Headers. A response header not listed here is
	// invisible to a `fetch` (e.g. the traceresponse trace id).
	ExposedHeaders []string
}

// DefaultCORSConfig returns permissive CORS defaults suitable for development.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins:   []string{"*"},
		AllowMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:   []string{"Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders: []string{"traceresponse"},
		MaxAge:         "3600",
	}
}

// CORS returns middleware that handles Cross-Origin Resource Sharing.
// It uses an O(1) set lookup for allowed origins and responds to preflight
// OPTIONS requests with 204 No Content. When AllowOrigins is ["*"], all
// origins are permitted (no Vary header).
func CORS(cfg *CORSConfig) Middleware {
	methods := strings.Join(cfg.AllowMethods, ", ")
	headers := strings.Join(cfg.AllowHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")

	// Build a set for O(1) origin lookup
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*"
	originSet := make(map[string]struct{}, len(cfg.AllowOrigins))
	for _, o := range cfg.AllowOrigins {
		originSet[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else if origin != "" {
				if _, ok := originSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
				}
			}

			w.Header().Set("Access-Control-Allow-Methods", methods)
			w.Header().Set("Access-Control-Allow-Headers", headers)
			if exposed != "" {
				w.Header().Set("Access-Control-Expose-Headers", exposed)
			}
			w.Header().Set("Access-Control-Max-Age", cfg.MaxAge)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
