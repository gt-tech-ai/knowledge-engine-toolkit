package interfaces

import "net/http"

// ServerTransport is a lifecycle-managed, health-checkable HTTP/RPC server: it is
// started and stopped by a lifecycle.Manager, health-probed by Kubernetes
// (Liveness/Readiness), and exposes handler registration plus its request mux. The
// backend (Connect over net/http today) is selected by the transport tier's Kind,
// so switching server frameworks is a config change, not a caller edit.
type ServerTransport interface {
	// Client supplies the Start/Stop lifecycle and Kubernetes health probes.
	Client

	// RegisterFunc mounts an http.HandlerFunc at a path pattern.
	RegisterFunc(pattern string, handler http.HandlerFunc)

	// Mux returns the underlying request multiplexer for direct handler registration
	// (e.g. mounting a generated Connect handler at its path prefix).
	Mux() *http.ServeMux
}
