package wire

import "google.golang.org/grpc/metadata"

// MetadataCarrier adapts gRPC metadata to the OTel TextMapCarrier, so the global
// propagator injects (a client) and extracts (a server) W3C trace context over gRPC
// metadata — the gRPC analog of propagation.HeaderCarrier for HTTP and Connect.
type MetadataCarrier struct {
	// MD is the metadata the trace context is read from or injected into.
	MD metadata.MD
}

// Get returns the first value for key (gRPC lower-cases metadata keys).
func (c MetadataCarrier) Get(key string) string {
	if v := c.MD.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// Set stores value under key, replacing any existing values.
func (c MetadataCarrier) Set(key, value string) { c.MD.Set(key, value) }

// Keys lists the carrier's metadata keys.
func (c MetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.MD))
	for k := range c.MD {
		keys = append(keys, k)
	}
	return keys
}
