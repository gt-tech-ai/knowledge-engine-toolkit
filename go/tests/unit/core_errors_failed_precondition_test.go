package unit_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// TestFailedPrecondition_MapsAtEveryEdge tests that the precondition code keeps its
// own gRPC status and an HTTP 400 on the way out and on the way back in.
//
// Why this test is important:
//   - A caller tells "fix the request" (INVALID_ARGUMENT) from "the system is not
//     ready for it" (FAILED_PRECONDITION) by the wire status alone; a precondition
//     surfacing as 500 reads as a server bug, and one folded into INVALID_ARGUMENT
//     sends the caller to fix input that is fine
//
// What it tests:
//   - CodeFailedPrecondition has the wire value "FAILED_PRECONDITION"
//   - ToHTTPStatus returns 400
//   - rpc.Sanitize returns connect.CodeFailedPrecondition with the fixed message
//   - a gRPC FAILED_PRECONDITION status maps back to CodeFailedPrecondition
//   - the code is neither transient nor permanent
func TestFailedPrecondition_MapsAtEveryEdge(t *testing.T) {
	t.Parallel()

	err := apperr.New(apperr.CodeFailedPrecondition, "population below the floor")

	assert.Equal(
		t,
		"FAILED_PRECONDITION",
		string(apperr.CodeFailedPrecondition),
	)
	assert.Equal(
		t,
		http.StatusBadRequest,
		apperr.ToHTTPStatus(apperr.CodeFailedPrecondition),
	)

	code, msg := rpc.Sanitize(err)
	assert.Equal(t, connect.CodeFailedPrecondition, code)
	assert.Equal(t, "failed precondition", msg)

	back := rpc.FromRPCError(status.Error(codes.FailedPrecondition, "stale revision"))
	assert.Equal(t, apperr.CodeFailedPrecondition, apperr.Code(back))

	assert.False(t, apperr.IsTransient(err))
	assert.False(t, apperr.IsPermanent(err))
}
