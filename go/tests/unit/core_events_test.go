// Package unit_test holds the black-box unit tests; this file verifies the go/core/events
// package: a consumer's event types parse through a Registry, and malformed or
// unregistered payloads and misregistrations are rejected. No network I/O occurs.
package unit_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/events"
)

// widgetCreated is a consumer-owned event type, standing in for an application's own
// catalog.
type widgetCreated struct {
	events.EventMetadata

	WidgetID string `json:"widget_id"`
}

func (e *widgetCreated) Type() events.EventType         { return "widget.created" }
func (e *widgetCreated) Metadata() events.EventMetadata { return e.EventMetadata }

// widgetValue implements Event with value receivers, so a factory can return it by value
// — a type Parse could never decode into.
type widgetValue struct {
	events.EventMetadata
}

func (widgetValue) Type() events.EventType           { return "widget.value" }
func (e widgetValue) Metadata() events.EventMetadata { return e.EventMetadata }

// newWidgetRegistry returns a registry with the consumer's widget.created event
// registered.
func newWidgetRegistry(tb testing.TB) *events.Registry {
	tb.Helper()
	reg := events.NewRegistry()
	require.NoError(
		tb,
		reg.Register("widget.created", func() events.Event { return &widgetCreated{} }),
	)
	return reg
}

// TestRegistryParsesConsumerEvents tests that a consumer parses its own event types
// through a registry instead of a closed catalog, and that misuse fails with
// CodeInvalidInput.
//
// Why this test is important:
//   - A library must not own an application's event catalog; the consumer registers its
//     types and the registry dispatches payloads to them.
//   - A misregistration must fail at registration: surfacing per message as INVALID_INPUT
//     looks like a bad payload, so consumers dead-letter every message of that type.
//
// What it tests:
//   - A registered type parses into the consumer's concrete event with metadata and body.
//   - An unregistered type, a missing event_type, a malformed payload and a body that
//     does not decode into the event are each rejected with CodeInvalidInput.
//   - A duplicate registration, a nil factory, an empty event type and a factory that
//     does not return a non-nil pointer are each rejected with CodeInvalidInput.
func TestRegistryParsesConsumerEvents(t *testing.T) {
	t.Parallel()
	reg := newWidgetRegistry(t)

	evt, err := reg.Parse(
		[]byte(
			`{"event_id":"e1","event_type":"widget.created","tenant_id":"t1","widget_id":"w1"}`,
		),
	)
	require.NoError(t, err)
	got, ok := evt.(*widgetCreated)
	require.True(t, ok, "want *widgetCreated, got %T", evt)
	assert.Equal(t, "w1", got.WidgetID)
	assert.Equal(t, "e1", got.Metadata().EventID)
	assert.Equal(t, "t1", got.Metadata().TenantID)

	for name, payload := range map[string]string{
		"unregistered type":  `{"event_type":"widget.deleted"}`,
		"missing event_type": `{"widget_id":"w1"}`,
		"malformed payload":  `{not json`,
		"undecodable body":   `{"event_type":"widget.created","widget_id":123}`,
	} {
		_, err = reg.Parse([]byte(payload))
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%s: %v", name, err)
	}

	for name, register := range map[string]func() error{
		"duplicate": func() error {
			return reg.Register("widget.created", func() events.Event { return &widgetCreated{} })
		},
		"nil factory": func() error { return reg.Register("widget.updated", nil) },
		"empty type": func() error {
			return reg.Register("", func() events.Event { return &widgetCreated{} })
		},
		"value factory": func() error {
			return reg.Register("widget.value", func() events.Event { return widgetValue{} })
		},
		"nil pointer factory": func() error {
			return reg.Register("widget.nil", func() events.Event { return (*widgetCreated)(nil) })
		},
		"nil event factory": func() error {
			return reg.Register("widget.none", func() events.Event { return nil })
		},
	} {
		err = register()
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%s: %v", name, err)
	}
}

// TestRegistry_ZeroValueAndConcurrentUse tests that a Registry needs no constructor and
// is safe for concurrent use, as its doc promises.
//
// Why this test is important:
//   - A `var reg events.Registry` at a composition root must not panic on its first
//     Register ("assignment to entry in nil map").
//   - Consumers register and parse from many goroutines; a data race corrupts the factory
//     map (caught by `go test -race`, which CI runs).
//
// What it tests:
//   - A zero-value Registry registers and parses an event.
//   - Concurrent Register and Parse calls all succeed and every registered type parses.
func TestRegistry_ZeroValueAndConcurrentUse(t *testing.T) {
	t.Parallel()
	var reg events.Registry
	require.NoError(
		t,
		reg.Register("widget.created", func() events.Event { return &widgetCreated{} }),
	)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, 2*workers)
	for i := range workers {
		wg.Go(func() {
			errs <- reg.Register(events.EventType(fmt.Sprintf("widget.v%d", i)),
				func() events.Event { return &widgetCreated{} })
		})
		wg.Go(func() {
			_, err := reg.Parse([]byte(`{"event_type":"widget.created"}`))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for i := range workers {
		_, err := reg.Parse(fmt.Appendf(nil, `{"event_type":"widget.v%d"}`, i))
		assert.NoError(t, err, "widget.v%d must be registered", i)
	}
}

// FuzzRegistryRoundTrip fuzzes the marshal → Registry.Parse round trip of a registered
// event.
//
// Why this test is important:
//   - Events cross process boundaries as JSON; a registry that loses or alters a field on
//     the way back (escaping, unicode, NUL bytes) silently corrupts what consumers act
//     on.
//
// What it tests:
//   - For arbitrary event id, tenant id and body strings, a marshaled widget.created
//     event parses back equal to its JSON-decoded form (JSON replaces invalid UTF-8 with
//     U+FFFD).
func FuzzRegistryRoundTrip(f *testing.F) {
	f.Add("e1", "t1", "w1")
	f.Add("", "", "")
	f.Add("é\"\\", "tenant/with/slash", "\x00")
	reg := newWidgetRegistry(f)

	f.Fuzz(func(t *testing.T, eventID, tenantID, widgetID string) {
		in := &widgetCreated{
			EventMetadata: events.EventMetadata{
				EventID:   eventID,
				EventType: "widget.created",
				TenantID:  tenantID,
			},
			WidgetID: widgetID,
		}
		data, err := json.Marshal(in)
		require.NoError(t, err)
		out, err := reg.Parse(data)
		require.NoError(t, err)
		// JSON replaces invalid UTF-8 with U+FFFD, so compare against the decoded form.
		var want widgetCreated
		require.NoError(t, json.Unmarshal(data, &want))
		assert.Equal(t, &want, out)
	})
}
