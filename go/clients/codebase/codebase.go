// Package codebase builds a Codebase from config: the in-memory kind (the default), a
// plain directory, or a git working tree, each composed with the foundation/codebase
// views and run through the client tier (Decorate). The sources live in the memory/,
// fs/ and git/ subpackages.
package codebase

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/fs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/git"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/memory"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	views "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindMemory serves Deps.Files from memory (the default).
	KindMemory client.Kind = "memory"

	// KindFS serves every file under Config.Root except `.git`.
	KindFS client.Kind = "fs"

	// KindGit serves the git working tree at Config.Root: tracked plus untracked,
	// non-ignored files.
	KindGit client.Kind = "git"
)

// Config selects and locates the repository.
type Config struct {
	// Kind selects the source; empty means KindMemory.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("codebase").
	Name string `yaml:"name" mapstructure:"name"`

	// Root is the repository root for KindFS and KindGit.
	Root string `yaml:"root" mapstructure:"root"`
}

// Deps carries the codebase's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config and supplies its
	// observability.
	decorate.Decorations

	// Runner runs git for KindGit.
	Runner interfaces.CommandRunner

	// Renderer renders charts for Rendered; nil makes Rendered CodeUnavailable.
	Renderer interfaces.Renderer

	// Snapshot is the run's memo; nil starts a new one keyed by Config.Root.
	Snapshot *views.Snapshot

	// Files are KindMemory's contents, by repository-relative path.
	Files map[string]string
}

// DefaultConfig returns the memory kind.
func DefaultConfig() Config { return Config{} }

// kinds is the codebase source table.
func kinds() client.Kinds[Config, Deps, interfaces.Codebase] {
	return client.Kinds[Config, Deps, interfaces.Codebase]{
		Family:   "codebase",
		Default:  KindMemory,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.Codebase]{
			KindMemory: newMemory,
			KindFS:     newFS,
			KindGit:    newGit,
		},
	}
}

// newMemory builds the memory kind over Deps.Files.
func newMemory(_ context.Context, cfg Config, deps Deps) (interfaces.Codebase, error) {
	return compose(memory.New(deps.Files), cfg, deps), nil
}

// newFS builds the plain-directory kind at Config.Root.
func newFS(_ context.Context, cfg Config, deps Deps) (interfaces.Codebase, error) {
	if cfg.Root == "" {
		return nil, apperr.New(apperr.CodeInvalidInput, "codebase fs: no root")
	}
	return compose(fs.New(cfg.Root), cfg, deps), nil
}

// newGit builds the git working-tree kind at Config.Root over Deps.Runner.
func newGit(_ context.Context, cfg Config, deps Deps) (interfaces.Codebase, error) {
	if cfg.Root == "" || deps.Runner == nil {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"codebase git: needs a root and a command runner")
	}
	return compose(git.New(cfg.Root, deps.Runner), cfg, deps), nil
}

// compose builds the views over src on the run's snapshot.
func compose(src interfaces.CodebaseSource, cfg Config, deps Deps) interfaces.Codebase {
	snap := deps.Snapshot
	if snap == nil {
		snap = views.NewSnapshot(cfg.Root)
	}
	return views.New(src, snap, deps.Renderer)
}

// NewFromConfig builds the codebase cfg.Kind selects and runs it through the client tier.
// An unknown kind, or a kind missing its root or runner, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.Codebase, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
