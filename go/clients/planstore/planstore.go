// Package planstore builds the saved-plan store from config: local (the default), which
// leaves the plans where they were written, or object, which keeps them in an object
// store behind a SHA-256 manifest so a later job on another machine applies exactly
// them. Every store runs through the client tier (Decorate).
package planstore

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindLocal keeps the plans in place: Save and Load do nothing (the default).
	KindLocal client.Kind = "local"

	// KindObject keeps the plans in an object store (Deps.Storage).
	KindObject client.Kind = "object"
)

// Config selects the store.
type Config struct {
	// Kind selects the backend; empty means KindLocal.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the store in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("planstore").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the store's collaborators.
type Deps struct {
	// Decorations decorates the store and supplies its observability.
	decorate.Decorations

	// Storage is the object store KindObject keeps the plans in; its own config
	// carries the server-side encryption every upload gets.
	Storage interfaces.StorageClient

	// Log records each plan kept and restored; nil logs nothing.
	Log interfaces.Logger
}

// kinds is the backend table.
func kinds() client.Kinds[Config, Deps, interfaces.PlanStore] {
	return client.Kinds[Config, Deps, interfaces.PlanStore]{
		Family:   "planstore",
		Default:  KindLocal,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.PlanStore]{
			KindLocal: func(context.Context, Config, Deps) (interfaces.PlanStore, error) {
				return Local{}, nil
			},
			KindObject: newObject,
		},
	}
}

// NewFromConfig builds the store cfg.Kind selects and runs it through the client tier;
// an unknown kind, or KindObject with no storage client, is CodeInvalidInput. It does
// no I/O.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.PlanStore, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

// FindPlans lists the files named file under dir, in walk order; a missing dir is
// CodeInvalidInput.
func FindPlans(dir, file string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == file {
			found = append(found, path)
		}
		return err
	})
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"read the saved plans under "+dir,
		)
	}
	return found, nil
}

// Local keeps the plans where they were written: the applying job reads the directory
// the planning job filled. Nothing leaves the machine.
type Local struct{}

// Compile-time assertion that Local satisfies PlanStore.
var _ interfaces.PlanStore = Local{}

// Save does nothing.
func (Local) Save(context.Context, types.PlanSet) error { return nil }

// Load does nothing.
func (Local) Load(context.Context, types.PlanSet) error { return nil }
