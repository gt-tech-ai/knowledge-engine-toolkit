package events

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Factory returns a new, zero-valued event of one type, for Registry.Parse to
// decode a payload into. It must return a non-nil pointer.
type Factory func() Event

// Registry maps event types to factories, so a consumer parses its own event
// catalog: register each type once at the composition root, then Parse payloads.
// EventMetadata is the envelope every event shares; its event_type field selects
// the factory. The zero value is an empty Registry ready to use (NewRegistry is
// equivalent), and a Registry is safe for concurrent use.
type Registry struct {
	// factories holds the registered factory per event type; nil until the first
	// Register.
	factories map[EventType]Factory
	// mu guards factories.
	mu sync.RWMutex
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[EventType]Factory)}
}

// Register adds the factory for eventType. It returns a CodeInvalidInput error,
// registering nothing, for an empty eventType, a type registered twice, a nil
// factory, or a factory that does not return a non-nil pointer (Parse decodes into
// the factory's value, so anything else would fail every message of that type).
func (r *Registry) Register(eventType EventType, factory Factory) error {
	if eventType == "" {
		return apperr.InvalidInput("event type must not be empty")
	}
	if err := checkFactory(eventType, factory); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[eventType]; exists {
		return apperr.InvalidInput(
			fmt.Sprintf("event type %q already registered", eventType),
		)
	}
	if r.factories == nil {
		r.factories = make(map[EventType]Factory)
	}
	r.factories[eventType] = factory
	return nil
}

// checkFactory returns a CodeInvalidInput error unless factory is non-nil and
// returns a non-nil pointer that a payload can be decoded into.
func checkFactory(eventType EventType, factory Factory) error {
	if factory == nil {
		return apperr.InvalidInput(
			fmt.Sprintf("nil factory for event type %q", eventType),
		)
	}
	v := reflect.ValueOf(factory())
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return apperr.InvalidInput(fmt.Sprintf(
			"factory for event type %q must return a non-nil pointer, got %s",
			eventType, v.Kind(),
		))
	}
	return nil
}

// Parse decodes a JSON event payload into the event registered for its
// event_type. A malformed payload, a missing event_type, an unregistered type or a
// body that does not decode into the event returns a CodeInvalidInput error. The
// payload is decoded twice (once to read the type, once into the concrete event);
// a consumer whose catalog is fixed and hot can keep a single-pass parser of its
// own.
func (r *Registry) Parse(data []byte) (Event, error) {
	var envelope struct {
		EventType EventType `json:"event_type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse event")
	}
	if envelope.EventType == "" {
		return nil, apperr.InvalidInput("event payload has no event_type")
	}
	r.mu.RLock()
	factory, ok := r.factories[envelope.EventType]
	r.mu.RUnlock()
	if !ok {
		return nil, apperr.InvalidInput(
			fmt.Sprintf("unknown event type: %s", envelope.EventType),
		)
	}
	event := factory()
	if err := json.Unmarshal(data, event); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse event")
	}
	return event, nil
}
