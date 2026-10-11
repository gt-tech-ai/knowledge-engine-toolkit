package interfaces

// ConfigLoader provides read-only access to loaded configuration values.
// Implementations handle the loading mechanics (files, env vars, remote
// sources); callers interact only through this interface.
//
// The interface deliberately omits a Load() method. Loading is a one-time
// setup concern handled by the concrete type's constructor or factory.
// Callers receive an already-loaded ConfigLoader ready for use.
type ConfigLoader interface {
	// Get returns the raw value for a key, or nil if not set.
	Get(key string) any

	// GetString returns the string value for a key.
	GetString(key string) string

	// GetInt returns the int value for a key.
	GetInt(key string) int

	// GetBool returns the bool value for a key.
	GetBool(key string) bool

	// Unmarshal decodes the full configuration into the target struct.
	Unmarshal(target any) error

	// UnmarshalKey decodes a specific configuration section into the target struct.
	// The key parameter is a dot-separated path (e.g., "database", "server.identity").
	UnmarshalKey(key string, target any) error
}
