// Package options provides a generic functional options pattern for configuring types.
//
// The options pattern allows flexible, extensible configuration of structs without
// breaking API compatibility. Instead of many constructor variants or large config
// structs, consumers pass Option functions that modify the target.
//
// Example usage:
//
//	type Config struct {
//	    Timeout time.Duration
//	    Retries int
//	}
//
//	func WithTimeout(d time.Duration) options.Option[Config] {
//	    return func(c *Config) { c.Timeout = d }
//	}
//
//	cfg := &Config{}
//	options.ApplyOptions(cfg, WithTimeout(5*time.Second))
package options

// Option is a function that modifies a value of type T.
// It follows the functional options pattern, allowing flexible configuration
// without constructor parameter explosion.
type Option[T any] func(*T)

// ApplyOptions applies a list of Option functions to a target value.
// Nil options are silently skipped. Options are applied in order, so later
// options can override earlier ones.
//
// Example:
//
//	cfg := &Config{Retries: 3}
//	ApplyOptions(cfg,
//	    WithRetries(5),      // Sets Retries to 5
//	    WithRetries(10),     // Overrides to 10
//	    nil,                 // Skipped
//	)
//	// cfg.Retries == 10
func ApplyOptions[T any](target *T, opts ...Option[T]) {
	for _, opt := range opts {
		if opt != nil {
			opt(target)
		}
	}
}

// OptionsProvider is an interface for types that can provide a list of options.
// This is useful for cloning configurations or composing option sets.
//
// Example:
//
//	type Component struct {
//	    retries int
//	    timeout time.Duration
//	}
//
//	func (c *Component) Options() []options.Option[Component] {
//	    return []options.Option[Component]{
//	        func(target *Component) { target.retries = c.retries },
//	        func(target *Component) { target.timeout = c.timeout },
//	    }
//	}
//
//	// Clone a component:
//	cloned := &Component{}
//	ApplyOptions(cloned, original.Options()...)
//
// interface-composition exemption — foundation options primitive: a single Options() →
// []Option[T] self-cloning port, not an id-CRUD data-access surface, so it embeds no
// foundation generic.
type OptionsProvider[T any] interface {
	// Options returns a slice of Option functions that capture the current
	// state of the provider. Applying these options to a zero-value T will
	// clone the provider's configuration.
	Options() []Option[T]
}
