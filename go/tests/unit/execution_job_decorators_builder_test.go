package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// captureLogger returns a MockLogger that appends each logged message (prefixed by
// its level: D/I/W/E) to *msgs, so a test can assert what was logged without a
// hand-rolled logger double.
func captureLogger(ctrl *gomock.Controller, msgs *[]string) *mocks.MockLogger {
	l := mocks.NewMockLogger(ctrl)
	l.EXPECT().Debug(gomock.Any(), gomock.Any()).
		Do(func(m string, _ ...any) { *msgs = append(*msgs, "D:"+m) }).AnyTimes()
	l.EXPECT().Info(gomock.Any(), gomock.Any()).
		Do(func(m string, _ ...any) { *msgs = append(*msgs, "I:"+m) }).AnyTimes()
	l.EXPECT().Warn(gomock.Any(), gomock.Any()).
		Do(func(m string, _ ...any) { *msgs = append(*msgs, "W:"+m) }).AnyTimes()
	l.EXPECT().Error(gomock.Any(), gomock.Any()).
		Do(func(m string, _ ...any) { *msgs = append(*msgs, "E:"+m) }).AnyTimes()
	l.EXPECT().With(gomock.Any()).Return(l).AnyTimes()
	l.EXPECT().WithContext(gomock.Any()).Return(l).AnyTimes()
	return l
}

// TestDecoratorBuilder_NoLogger_ReturnsInner tests that Wrap(...).Build() with
// no decorators added returns the original job unchanged.
//
// Why this test is important:
//   - The decorator chain must be zero-cost when nothing is configured;
//     wrapping the job in a pass-through layer anyway would add overhead and an
//     extra stack frame for no behavioural gain. Callers rely on Build being an
//     identity when no decorators are requested.
//
// What it tests:
//   - The built job's Meta().Name equals the inner job's name, evidencing that
//     the inner job is returned directly.
func TestDecoratorBuilder_NoLogger_ReturnsInner(t *testing.T) {
	ctrl := gomock.NewController(t)
	// No Execute expectation: Wrap(...).Build() with no decorators must return the
	// inner job untouched, so only its identity (Meta) is read, never its Execute.
	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(types.JobMeta{Name: "lint"}).AnyTimes()
	j := decorators.Wrap(inner).Build()

	assert.Equal(t, "lint", j.Meta().Name)
}

// TestDecoratorBuilder_WithLogger_LogsStartAndFinish tests that a logger-wrapped
// job emits a start and a finish log entry around a successful execution while
// passing the inner result through untouched.
//
// Why this test is important:
//   - The logging decorator is how job execution becomes observable in
//     structured logs; missing the start/finish bracket would leave operators
//     unable to trace which jobs ran or how long they took. Equally, the
//     decorator must not alter the result it wraps.
//
// What it tests:
//   - After Execute on a passing job, the logger captured both "job started" and
//     "job finished" entries, and the returned result is the inner job's single
//     Pass result.
func TestDecoratorBuilder_WithLogger_LogsStartAndFinish(t *testing.T) {
	ctrl := gomock.NewController(t)
	var msgs []string
	logger := captureLogger(ctrl, &msgs)
	// The inner job runs once and passes through a single Pass result; the logger
	// decorator must bracket that call with start/finish entries.
	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(types.JobMeta{Name: "lint"}).AnyTimes()
	inner.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.StepResults{{Status: types.StatusPass}}, nil).
		Times(1)

	j := decorators.Wrap(inner).WithLogger(logger).Build()

	assert.Equal(t, "lint", j.Meta().Name)

	results, err := j.Execute(context.Background(), nil, "/root")
	require.NoError(t, err)
	assert.False(
		t,
		len(results) != 1 || results[0].Status != types.StatusPass,
		"expected Pass result, got %v",
		results,
	)

	var hasStart, hasFinish bool
	for _, m := range msgs {
		if m == "I:job started" {
			hasStart = true
		}
		if m == "I:job finished" {
			hasFinish = true
		}
	}
	assert.True(t, hasStart, "expected 'job started' log, got %v", msgs)
	assert.True(t, hasFinish, "expected 'job finished' log, got %v", msgs)
}

// TestDecoratorBuilder_WithLogger_LogsErrorOnFailure tests that a logger-wrapped
// job logs at error level (not finish level) when the inner job returns an error.
//
// Why this test is important:
//   - A failing job must be distinguishable in the logs from a successful one;
//     logging an error as a normal "finished" entry would bury failures and
//     undermine log-based alerting and post-mortem triage.
//
// What it tests:
//   - When the inner job returns an error, Execute propagates that error and the
//     logger captured a "job error" entry.
func TestDecoratorBuilder_WithLogger_LogsErrorOnFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	var msgs []string
	logger := captureLogger(ctrl, &msgs)
	inner := mocks.NewMockAnyJob(ctrl)
	inner.EXPECT().Meta().Return(types.JobMeta{Name: "broken"}).AnyTimes()
	inner.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, apperr.Sentinel("command failed")).AnyTimes()

	j := decorators.Wrap(inner).WithLogger(logger).Build()

	_, err := j.Execute(context.Background(), nil, "/root")
	require.Error(t, err)

	var hasErr bool
	for _, m := range msgs {
		if m == "E:job error" {
			hasErr = true
		}
	}
	assert.True(t, hasErr, "expected 'job error' log entry, got %v", msgs)
}
