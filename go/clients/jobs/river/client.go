package river

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.JobEnqueuer = (*Client)(nil)

// Client is a no-op JobEnqueuer: it opens no pool and queues nothing. Use
// InsertClient to enqueue durably through River.
type Client struct{}

// NewClient creates the no-op job client; cfg is not read.
func NewClient(_ Config) (*Client, error) {
	return &Client{}, nil
}

// Enqueue accepts job without queueing it and returns nil.
func (c *Client) Enqueue(_ context.Context, _ interfaces.Job) error {
	return nil
}

// Close closes the job client.
func (c *Client) Close() error {
	return nil
}
