package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// testConfig is a sample configuration struct for testing options.
type testConfig struct {
	Name        string
	MaxRetries  int
	Timeout     time.Duration
	EnableDebug bool
}

// withMaxRetries is a sample option function.
func withMaxRetries(n int) options.Option[testConfig] {
	return func(c *testConfig) {
		c.MaxRetries = n
	}
}

// withTimeout is a sample option function.
func withTimeout(d time.Duration) options.Option[testConfig] {
	return func(c *testConfig) {
		c.Timeout = d
	}
}

// withDebug is a sample option function.
func withDebug(enabled bool) options.Option[testConfig] {
	return func(c *testConfig) {
		c.EnableDebug = enabled
	}
}

// withName is a sample option function.
func withName(name string) options.Option[testConfig] {
	return func(c *testConfig) {
		c.Name = name
	}
}

// TestApplyOptions tests that the generic ApplyOptions function correctly
// applies functional options to a config struct.
//
// Why this test is important:
//   - ApplyOptions is the foundation of every factory in the codebase; if it
//     fails to apply options or panics on nil, all builders break
//   - Ordering guarantees ensure that override semantics (last-writer-wins)
//     work correctly for config layering
//
// What it tests:
//   - Single option is applied
//   - Multiple options are applied in registration order
//   - Later options override earlier ones for the same field
//   - Empty option list leaves config unchanged
//   - Nil options in the list are skipped without panic
func TestApplyOptions(t *testing.T) {
	t.Parallel()

	t.Run("applies single option", func(t *testing.T) {
		t.Parallel()
		cfg := &testConfig{MaxRetries: 3}
		options.ApplyOptions(cfg, withMaxRetries(5))
		assert.Equal(t, 5, cfg.MaxRetries)
	})

	t.Run("applies multiple options in order", func(t *testing.T) {
		t.Parallel()
		cfg := &testConfig{}
		options.ApplyOptions(
			cfg,
			withMaxRetries(3),
			withTimeout(30*time.Second),
			withDebug(true),
			withName("test"),
		)

		assert.Equal(t, 3, cfg.MaxRetries)
		assert.Equal(t, 30*time.Second, cfg.Timeout)
		assert.True(t, cfg.EnableDebug, "EnableDebug should be true")
		assert.Equal(t, "test", cfg.Name)
	})

	t.Run("later options override earlier ones", func(t *testing.T) {
		t.Parallel()
		cfg := &testConfig{}
		options.ApplyOptions(
			cfg,
			withMaxRetries(3),
			withMaxRetries(5),
			withMaxRetries(10),
		)
		assert.Equal(t, 10, cfg.MaxRetries, "last option wins")
	})

	t.Run("handles empty option list", func(t *testing.T) {
		t.Parallel()
		cfg := &testConfig{MaxRetries: 3}
		options.ApplyOptions(cfg)
		assert.Equal(t, 3, cfg.MaxRetries, "no options should leave config unchanged")
	})

	t.Run("handles nil options in list", func(t *testing.T) {
		t.Parallel()
		cfg := &testConfig{}
		var trailing options.Option[testConfig]
		options.ApplyOptions(
			cfg,
			withMaxRetries(3),
			nil,
			withTimeout(30*time.Second),
			trailing,
		)
		assert.Equal(t, 3, cfg.MaxRetries)
		assert.Equal(t, 30*time.Second, cfg.Timeout)
	})
}

// testComponent implements OptionsProvider to test the interface.
type testComponent struct {
	maxRetries int
	timeout    time.Duration
	debug      bool
}

func (c *testComponent) Options() []options.Option[testComponent] {
	return []options.Option[testComponent]{
		func(target *testComponent) { target.maxRetries = c.maxRetries },
		func(target *testComponent) { target.timeout = c.timeout },
		func(target *testComponent) { target.debug = c.debug },
	}
}

// TestOptionsProvider tests that a type implementing OptionsProvider can export
// its state as options for cloning.
//
// Why this test is important:
//   - OptionsProvider enables config round-tripping (export then re-apply); a
//     broken provider would silently lose settings during cloning
//
// What it tests:
//   - Options() returns a slice that, when applied to a blank struct, reproduces
//     all fields of the original
func TestOptionsProvider(t *testing.T) {
	t.Parallel()

	t.Run("component implements OptionsProvider", func(t *testing.T) {
		t.Parallel()

		var _ options.OptionsProvider[testComponent] = (*testComponent)(nil)

		original := &testComponent{
			maxRetries: 5,
			timeout:    10 * time.Second,
			debug:      true,
		}

		cloned := &testComponent{}
		options.ApplyOptions(cloned, original.Options()...)

		assert.Equal(t, original.maxRetries, cloned.maxRetries)
		assert.Equal(t, original.timeout, cloned.timeout)
		assert.Equal(t, original.debug, cloned.debug)
	})
}

// TestOptionTypeAlias tests that Option[T] is a function type alias that can
// be assigned from a plain function literal.
//
// Why this test is important:
//   - Option[T] is the generic type alias underlying all functional options;
//     if the alias breaks, no factory in the codebase compiles
//
// What it tests:
//   - A plain function literal is assignable to options.Option[testConfig]
//   - Calling the option mutates the config correctly
func TestOptionTypeAlias(t *testing.T) {
	t.Parallel()

	var opt options.Option[testConfig] = func(c *testConfig) {
		c.MaxRetries = 99
	}

	cfg := &testConfig{}
	opt(cfg)

	assert.Equal(t, 99, cfg.MaxRetries)
}
