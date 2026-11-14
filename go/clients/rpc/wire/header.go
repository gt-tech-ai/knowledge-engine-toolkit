package wire

import "connectrpc.com/connect/v2"

// HeaderCarrier adapts a Connect *connect.Header (a call's request or response
// metadata, read from its connect.CallInfo) to OpenTelemetry's
// propagation.TextMapCarrier, so a server extracts the caller's W3C trace context and a
// client injects its own without copying the header into an http.Header.
type HeaderCarrier struct {
	// Header is the metadata read and written; it must not be nil.
	Header *connect.Header
}

// Get returns the first value of key.
func (c HeaderCarrier) Get(key string) string { return c.Header.Get(key) }

// Set replaces key's values with value.
func (c HeaderCarrier) Set(key, value string) { c.Header.Set(key, value) }

// Keys returns every key the header holds.
func (c HeaderCarrier) Keys() []string {
	keys := make([]string, 0, c.Header.Len())
	for key := range c.Header.All() {
		keys = append(keys, key)
	}

	return keys
}
