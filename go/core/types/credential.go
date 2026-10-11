package types

import (
	"fmt"
	"io"
)

// Ref is a backend-agnostic reference to a credential: the deploy environment, the
// credential class, and the field within that class. A credential Source resolves a
// Ref to a concrete backend address (an env var, a file key, or a secret-manager
// field) through its configured mapping, so callers never name a backend directly.
type Ref struct {
	// Env is the deploy environment the credential belongs to (e.g. "dev", "staging").
	Env string
	// Class is the credential class (e.g. "bootstrap", "runtime", "e2e-ropg").
	Class string
	// Field is the field within the class (e.g. "client_id", "client_secret", "domain").
	Field string
}

// String renders the reference (Env/Class/Field) for logs and errors. It carries no
// secret value — only the identifiers naming which credential is referenced.
func (r Ref) String() string {
	return fmt.Sprintf("%s/%s/%s", r.Env, r.Class, r.Field)
}

// secretMask is the fixed placeholder a Secret renders instead of its value.
const secretMask = "***"

// Secret is an opaque credential value. Every rendering path — String, the fmt verbs
// (via Format), and text/JSON marshaling — returns a fixed mask, so a Secret can never
// leak its value through a log line, a %v dump, or serialization. The raw value is
// reachable only through the explicit Reveal, called at the injection boundary (e.g.
// setting a child process's environment), never where output is logged.
type Secret struct {
	// value is the raw credential; unexported so it is reachable only via Reveal.
	value string
}

// NewSecret wraps a raw credential value in an opaque Secret.
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Reveal returns the raw credential value. Call it only at the point of use (e.g.
// injecting a child process's environment), never where the result may be logged.
func (s Secret) Reveal() string {
	return s.value
}

// IsZero reports whether the Secret holds no value (an unresolved/empty credential).
func (s Secret) IsZero() bool {
	return s.value == ""
}

// String returns the mask so a Secret printed through its Stringer never reveals its
// value.
func (s Secret) String() string {
	return secretMask
}

// Format renders the mask for every fmt verb (%s, %v, %q, %#v, …), so no format
// directive can print the underlying value.
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, secretMask)
}

// MarshalText masks the value for text encoders (YAML, encoding.TextMarshaler consumers).
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(secretMask), nil
}

// MarshalJSON masks the value for JSON encoders.
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"` + secretMask + `"`), nil
}
