// Package config provides a factory for ConfigLoader implementations with
// multiple backends.
//
// Use New() or NewFromConfig() to create a loaded config instance. The factory
// handles calling Load() internally, returning a ready-to-use ConfigLoader.
//
// Example:
//
//	// Create a Viper config loader pointing at a config directory
//	cfg, err := config.New(config.KindViper, config.WithBaseDir("./configs"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	port := cfg.GetInt("server.port")
//
//	// Create from config struct
//	c := config.DefaultConfig()
//	c.Viper.BaseDir = "./configs"
//	cfg, err := config.NewFromConfig(c)
package config

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	viperloader "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/viper"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which config loader implementation to use.
type Kind int

const (
	// KindViper uses a Viper-backed loader that reads hierarchical YAML
	// config: base.yaml -> {env}.yaml -> secrets.yaml -> env vars.
	KindViper Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindViper:
		return "viper"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates a ConfigLoader of the specified kind with optional functional
// options. The loader is fully initialized (Load called internally).
// Returns an error if the kind is unknown or loading fails.
//
// Example:
//
//	cfg, err := config.New(config.KindViper, config.WithBaseDir("./configs"))
//	if err != nil {
//	    return err
//	}
//	port := cfg.GetInt("server.port")
func New(kind Kind, opts ...options.Option[Config]) (interfaces.ConfigLoader, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a ConfigLoader from a Config struct. The loader is
// fully initialized (Load called internally). Returns an error if the kind is
// unknown or loading fails.
func NewFromConfig(cfg Config) (interfaces.ConfigLoader, error) {
	switch cfg.Kind {
	case KindViper:
		loader := viperloader.New(cfg.Viper)
		if err := loader.Load(); err != nil {
			return nil, err
		}
		return loader, nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown config kind: %v", cfg.Kind),
		)
	}
}
