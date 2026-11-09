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
		AllowOrigins:   []string{textStar},
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
	methods := strings.Join(cfg.AllowMethods, textCommaSpace)
	headers := strings.Join(cfg.AllowHeaders, textCommaSpace)
	exposed := strings.Join(cfg.ExposedHeaders, textCommaSpace)

	// Build a set for O(1) origin lookup
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == textStar
	originSet := make(map[string]struct{}, len(cfg.AllowOrigins))

	for _, o := range cfg.AllowOrigins {
		originSet[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			header := writer.Header()

			setAllowOrigin(
				header,
				request.Header.Get("Origin"),
				allowAllFlag(allowAll),
				originSet,
			)
			header.Set("Access-Control-Allow-Methods", methods)
			header.Set("Access-Control-Allow-Headers", headers)
			setExposeHeaders(header, exposed)
			header.Set("Access-Control-Max-Age", cfg.MaxAge)

			if request.Method == http.MethodOptions {
				writer.WriteHeader(http.StatusNoContent)

				return
			}

			next.ServeHTTP(writer, request)
		})
	}
}

// allowAllFlag names the allowAll option in a signature.
type allowAllFlag bool

// setAllowOrigin answers with a wildcard, or echoes an allowed request origin.
func setAllowOrigin(
	header http.Header,
	origin string,
	allowAll allowAllFlag,
	set map[string]struct{},
) {
	if allowAll {
		header.Set("Access-Control-Allow-Origin", textStar)

		return
	}

	if _, ok := set[origin]; ok && origin != "" {
		header.Set("Access-Control-Allow-Origin", origin)
		header.Set("Vary", "Origin")
	}
}

// setExposeHeaders sets the header only when headers are exposed.
func setExposeHeaders(h http.Header, exposed string) {
	if exposed != "" {
		h.Set("Access-Control-Expose-Headers", exposed)
	}
}
