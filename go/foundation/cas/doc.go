// Package cas is the content-addressing half of the engine's content-addressed store:
// a value's address is the SHA-256 of its domain, a NUL byte and its canonical bytes.
//
// The canonical form is RFC 8785 (JCS): object keys sorted, numbers and strings in one
// spelling, no insignificant whitespace. Two encoders that agree on a value therefore
// agree on its hash, whatever key order or spacing they wrote. A Domain such as
// "chartspec/v1" or "nlp-annotation/v1" prefixes the hashed bytes, so a value valid in
// two formats never shares an address, and its version changes only when that format's
// encoding does.
//
// Canonical canonicalizes JSON bytes, CanonicalOf marshals a value first, Sum addresses
// canonical bytes, and HashOf does both for a value. The stores that keep nodes and
// move refs live in stores/cas behind core/interfaces.NodeStore and RefStore.
package cas
