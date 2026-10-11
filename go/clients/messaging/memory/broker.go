// Package memory provides an in-process messaging backend for no-infra builds and tests.
//
// A Broker holds one buffered channel per topic; a Publisher and Subscriber connect only
// when handed the SAME broker instance (not package-global state), so two separate
// brokers are fully isolated. Mirrors the Python messaging/memory backend. Selected by
// the messaging tier Kind.
package memory

import (
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// topicBuffer is the per-topic channel capacity — large enough that a burst of publishes
// does not block the publisher before a subscriber drains them.
const topicBuffer = 1024

// Broker is an in-process message broker: a lazily-created buffered channel per topic.
type Broker struct {
	// topics holds one buffered channel per topic, created on first use.
	topics map[string]chan *interfaces.Message
	// mu guards concurrent access to topics.
	mu sync.Mutex
}

// NewBroker creates an empty in-process broker.
func NewBroker() *Broker {
	return &Broker{topics: make(map[string]chan *interfaces.Message)}
}

// topic returns the channel for name, creating it on first use.
func (b *Broker) topic(name string) chan *interfaces.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.topics[name]
	if ch == nil {
		ch = make(chan *interfaces.Message, topicBuffer)
		b.topics[name] = ch
	}
	return ch
}
