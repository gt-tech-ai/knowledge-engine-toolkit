package unit_test

import (
	"context"
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/awserr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/httperr"
)

// TestAWSErr_Classify tests the AWS SDK error coding every AWS client family shares.
//
// Why this test is important:
//   - The client tier retries by code: a throttle coded as permanent is never retried,
//     and a rejected request coded as transient is replayed until the breaker opens.
//
// What it tests:
//   - A throttle and a server fault are CodeUnavailable, an access denial
//     CodeForbidden, another client fault CodeInvalidInput; nil, a coded error and a
//     context error pass through unchanged.
func TestAWSErr_Classify(t *testing.T) {
	t.Parallel()
	server := &smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer}
	denied := &smithy.GenericAPIError{
		Code:  "AccessDeniedException",
		Fault: smithy.FaultClient,
	}
	cases := map[string]struct {
		err  error
		want apperr.ErrorCode
	}{
		"throttle": {err: &brtypes.ThrottlingException{}, want: apperr.CodeUnavailable},
		"server":   {err: server, want: apperr.CodeUnavailable},
		"denied":   {err: denied, want: apperr.CodeForbidden},
		"validation": {
			err:  &brtypes.ValidationException{},
			want: apperr.CodeInvalidInput,
		},
		"coded": {
			err:  apperr.New(apperr.CodeConflict, "x"),
			want: apperr.CodeConflict,
		},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, apperr.Code(awserr.Classify(tc.err)), name)
	}
	require.NoError(t, awserr.Classify(nil))
	assert.ErrorIs(t, awserr.Classify(context.Canceled), context.Canceled)
}

// TestHTTPErr_Transport tests the send-error coding every HTTP client family shares.
//
// Why this test is important:
//   - A refused connection must be retried, while the caller's own deadline or
//     cancellation must not be replayed.
//
// What it tests:
//   - A plain transport error is CodeUnavailable, a deadline CodeTimeout, a
//     cancellation CodeCanceled, and an already-coded error keeps its code.
func TestHTTPErr_Transport(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err  error
		want apperr.ErrorCode
	}{
		"refused": {
			err:  apperr.Sentinel("connection refused"),
			want: apperr.CodeUnavailable,
		},
		"deadline": {err: context.DeadlineExceeded, want: apperr.CodeTimeout},
		"canceled": {err: context.Canceled, want: apperr.CodeCanceled},
		"coded": {
			err:  apperr.New(apperr.CodeForbidden, "x"),
			want: apperr.CodeForbidden,
		},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, apperr.Code(httperr.Transport(tc.err, "send")), name)
	}
}
