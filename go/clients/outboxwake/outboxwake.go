// Package outboxwake builds the OutboxWaker an outbox relay listens to so it drains as
// soon as rows commit instead of at its next poll: the zero-infrastructure none waker
// (the default; the relay polls only) or a PostgreSQL LISTEN/NOTIFY listener, selected by
// Kind. A wake is only a hint, so the relay keeps polling whatever the kind.
package outboxwake

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outboxwake/pgnotify"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outboxwake/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

const (
	// KindNone never wakes the relay (the default).
	KindNone client.Kind = "none"

	// KindPgNotify wakes the relay on a PostgreSQL NOTIFY.
	KindPgNotify client.Kind = "pg_notify"
)

// Config selects and tunes the waker.
type Config struct {
	// Kind selects the backend; empty means KindNone.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Channel is the PostgreSQL channel KindPgNotify listens on; it must match the
	// outbox store's notify channel. Required for KindPgNotify.
	Channel string `yaml:"channel" mapstructure:"channel"`

	// Buffer is how many wakes KindPgNotify holds for a slow relay before it drops the
	// newest; zero means pgnotify.DefaultBuffer.
	Buffer int `yaml:"buffer" mapstructure:"buffer"`
}

// Deps carries the waker's runtime collaborators.
type Deps struct {
	// Logger records connects, disconnects and drops; nil logs nothing.
	Logger interfaces.Logger

	// Metrics records outbox_wake_total and outbox_wake_connected; nil records nothing.
	Metrics interfaces.Metrics

	// DSN is the connection string KindPgNotify dials for its own dedicated connection,
	// outside any pool. Required for KindPgNotify.
	DSN string
}

// kinds is the outbox-waker backend table.
func kinds() client.Kinds[Config, Deps, interfaces.OutboxWaker] {
	return client.Kinds[Config, Deps, interfaces.OutboxWaker]{
		Family:  "outbox waker",
		Default: KindNone,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.OutboxWaker]{
			KindNone: func(context.Context, Config, Deps) (interfaces.OutboxWaker, error) {
				return stub.New(), nil
			},
			KindPgNotify: client.RefConfig(newPgNotify),
		},
		Decorate:   nil,
		Options:    nil,
		AtMostOnce: nil,
	}
}

// NewFromConfig builds the waker cfg.Kind selects, with no I/O: a pg_notify waker dials
// only when started. An unknown kind or an invalid backend config is CodeInvalidInput.
// cfg must not be nil.
func NewFromConfig(
	ctx context.Context,
	cfg *Config,
	deps Deps,
) (interfaces.OutboxWaker, error) {
	return kinds().New(ctx, cfg.Kind, *cfg, deps)
}

// newPgNotify builds the LISTEN/NOTIFY waker.
func newPgNotify(
	_ context.Context,
	cfg *Config,
	deps Deps,
) (interfaces.OutboxWaker, error) {
	return pgnotify.New(
		pgnotify.Config{Channel: cfg.Channel, Buffer: cfg.Buffer},
		deps.DSN,
		deps.Logger,
		deps.Metrics,
	)
}
