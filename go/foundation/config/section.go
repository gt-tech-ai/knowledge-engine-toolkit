package config

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Section unmarshals the config section at key into a T and validates it, the shape of
// every "extract and validate a typed slice" provider in a composition root:
//
//	logging, err := config.Section[infra.LoggingConfig](loader, "logging")
//
// T's Validate may have a value or a pointer receiver. A section that does not decode
// or does not validate is CodeInvalidInput, naming key; a validation error that already
// carries a code keeps it.
func Section[T any, PT interface {
	*T
	Validate() error
}](loader interfaces.ConfigLoader, key string) (T, error) {
	var cfg T

	err := loader.UnmarshalKey(key, &cfg)
	if err != nil {
		return cfg, apperr.Wrap(err, apperr.CodeInvalidInput, "unmarshal "+key+" config")
	}

	err = PT(&cfg).Validate()
	if err != nil {
		var zero T

		return zero, apperr.Wrap(
			err, apperr.CodeOr(err, apperr.CodeInvalidInput), "invalid "+key+" config")
	}

	return cfg, nil
}
