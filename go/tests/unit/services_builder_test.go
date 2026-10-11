package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestOpServiceBuilder_NameAndResultPassThrough tests that a decorated OpService
// reports its name and returns the underlying operation's result and error
// unchanged (through the logging + recovery decorators).
//
// Why this test is important:
//   - Build is the chokepoint every CLI service is assembled through; if a
//     decorator altered the result or swallowed/mutated the error, every service's
//     success and failure reporting would be wrong (the silent-pass class of bug).
//
// What it tests:
//   - Name() returns the configured name; Run returns the operation's value on
//     success and the operation's error verbatim on failure, with WithLog enabled.
func TestOpServiceBuilder_NameAndResultPassThrough(t *testing.T) {
	t.Parallel()
	wantErr := apperr.Sentinel("boom")
	tests := []struct {
		wantErr error
		run     services.RunFunc[string, int]
		name    string
		wantOut int
	}{
		{
			name:    "success returns operation result",
			run:     func(_ context.Context, s string) (int, error) { return len(s), nil },
			wantOut: 5,
		},
		{
			name:    "failure passes the error through unchanged",
			run:     func(_ context.Context, _ string) (int, error) { return 0, wantErr },
			wantErr: wantErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			// A no-op logger: the logging decorator writes entry (Debug) and, on
			// failure, error (Error) — neither is asserted here, so both accept any
			// call. The observable contract under test is result/error pass-through.
			logger := mocks.NewMockLogger(ctrl)
			logger.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
			logger.EXPECT().Error(gomock.Any(), gomock.Any()).AnyTimes()
			svc := services.Build(tt.run).Named("calc").WithLog(logger).Service()
			assert.Equal(t, "calc", svc.Name())

			out, err := svc.Run(context.Background(), "hello")
			assert.Equal(t, tt.wantOut, out)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestOpServiceBuilder_RecoveryConvertsPanic tests that the always-outermost
// recovery decorator turns a panic in the operation into an error rather than
// letting it escape the service boundary.
//
// Why this test is important:
//   - A panic escaping a service crashes the request handler / CLI process; the
//     recovery decorator is the safety net that converts it to a returned error
//     naming the service, even when no logger is wired.
//
// What it tests:
//   - An operation that panics yields a non-nil error (no panic propagates) that
//     names the service and the panic, and a zero result — with no logger set.
func TestOpServiceBuilder_RecoveryConvertsPanic(t *testing.T) {
	t.Parallel()
	svc := services.Build(func(context.Context, string) (int, error) {
		panic("kaboom")
	}).Named("risky").Service() // no logger: recovery must still catch

	out, err := svc.Run(context.Background(), "x")
	require.Error(t, err, "panic is converted to an error, not propagated")
	assert.Equal(t, 0, out)
	assert.Contains(t, err.Error(), "risky")
	assert.Contains(t, err.Error(), "panicked")
}

// TestOpServiceBuilder_RecoveryLogsPanicWithLogger tests that when a logger is
// wired, the recovery decorator logs the recovered panic at error level in
// addition to converting it into a returned error.
//
// Why this test is important:
//   - The recovered panic is the operational signal on-call sees; without the
//     error-level log entry the panic vanishes silently and only the caller
//     learns of it, so the log branch of the recovery decorator must fire when a
//     logger is present.
//
// What it tests:
//   - A panicking service built WithLog records an error-level "service.Run
//     panicked" log entry and still returns the converted error with a zero result.
func TestOpServiceBuilder_RecoveryLogsPanicWithLogger(t *testing.T) {
	t.Parallel()
	spy := fixtures.NewSpyLogger()
	svc := services.Build(func(context.Context, string) (int, error) {
		panic("kaboom")
	}).Named("risky").WithLog(spy).Service()

	out, err := svc.Run(context.Background(), "x")

	require.Error(t, err, "panic is converted to an error, not propagated")
	assert.Equal(t, 0, out)
	assert.Contains(t, spy.ErrorCalls, "service.Run panicked",
		"recovery must log the panic at error level when a logger is present")
}

// TestBase_CarriesNameAndLogger tests that Base exposes the name and logger it
// was constructed with.
//
// Why this test is important:
//   - Every idiosyncratic CLI service embeds *Base and reads its name/logger
//     through these accessors instead of threading them through every call; wrong
//     wiring here mislabels logs and metrics for every service that embeds it.
//
// What it tests:
//   - NewBase(name, log) exposes the same name via Name() and the same logger via Log().
func TestBase_CarriesNameAndLogger(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	// The logger is only stored and read back, never invoked, so no expectations
	// are set — Base must return the exact logger it was constructed with.
	log := mocks.NewMockLogger(ctrl)
	b := services.NewBase("seed", log)
	assert.Equal(t, "seed", b.Name())
	assert.Equal(t, interfaces.Logger(log), b.Log())
}
