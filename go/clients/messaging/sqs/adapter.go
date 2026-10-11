package sqs

// Adapter provides AWS SQS-specific helpers over the SQS config.
type Adapter struct {
	// cfg holds the SQS configuration for queue resolution.
	cfg Config
}

// NewAdapter creates a new SQS adapter.
func NewAdapter(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

// QueueURL returns the SQS queue URL for a topic: the configured QueueURL base
// joined with the topic by "/".
func (a *Adapter) QueueURL(topic string) string {
	return a.cfg.QueueURL + "/" + topic
}
