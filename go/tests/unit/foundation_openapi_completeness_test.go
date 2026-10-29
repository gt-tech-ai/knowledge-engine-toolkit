package unit_test

// Black-box tests for the openapi codegen package's JSON:API completeness gate
// They drive openapi.CheckCompleteness — the collect-all sibling of
// BuildRouteTable — over the same self-contained synthetic FileDescriptorSet the
// route-table tests use (the descriptor builders in tests/fixtures: JSONAPIAnnotations,
// BuildRouteFDS, Method, ResourceMessage, EmptyMessage). No buf, no generated Go stubs.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestCheckCompleteness_FlagsUnannotatedRESTRPC tests that a REST RPC which cannot
// be classified into a JSON:API role, and is not exempt, is reported as a violation
// naming the RPC.
//
// Why this test is important:
//   - The gate exists to turn the generator's silent skip of an un-annotated REST
//
// RPC into a loud, named failure — so a new endpoint cannot ship
//
//	enveloped-or-not by accident.
//
// What it tests:
//   - A POST with no resource response and no action marker yields exactly one
//     Violation whose RPC is the offending method and whose Reason names it.
func TestCheckCompleteness_FlagsUnannotatedRESTRPC(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(
		t,
		s.FileOptsWithService(),
		[]*descriptorpb.DescriptorProto{fixtures.EmptyMessage("Empty")},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method("MarkRead", "Empty", "Empty",
				s.MethodOpts("POST", "/api/v1/notifications/mark-read", false)),
		},
	)

	violations, err := openapi.CheckCompleteness(fds, nil)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Equal(t, "test.v1.TestService.MarkRead", violations[0].RPC)
	assert.Contains(t, violations[0].Reason, "test.v1.TestService.MarkRead")
}

// TestCheckCompleteness_RespectsExemptMethods tests that an unclassifiable RPC named
// in the exemption set is not reported.
//
// Why this test is important:
//   - The exempt list (the SAME config DATA the generator reads) is what
//     lets a genuine action/internal RPC coexist with the gate; forking or ignoring
//     it would make the gate reject the real surface.
//
// What it tests:
//   - The same unclassifiable POST, when exempt, yields no violations.
func TestCheckCompleteness_RespectsExemptMethods(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(
		t,
		s.FileOptsWithService(),
		[]*descriptorpb.DescriptorProto{fixtures.EmptyMessage("Empty")},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method("MarkRead", "Empty", "Empty",
				s.MethodOpts("POST", "/api/v1/notifications/mark-read", false)),
		},
	)

	violations, err := openapi.CheckCompleteness(fds, map[string]bool{
		"test.v1.TestService.MarkRead": true,
	})
	require.NoError(t, err)
	assert.Empty(t, violations, "an exempt RPC is not a completeness violation")
}

// TestCheckCompleteness_PassesFullyAnnotatedSurface tests that a surface whose every
// REST RPC classifies cleanly produces no violations.
//
// Why this test is important:
//   - The gate ships enforcing; it must be green on a complete surface, or it would
//     block every build (the whole point is to fail only on a genuine gap).
//
// What it tests:
//   - The shared docMethods surface (LIST/GET_ONE/CREATE/UPDATE + a marked ACTION)
//     yields zero violations.
func TestCheckCompleteness_PassesFullyAnnotatedSurface(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(
		t,
		s.FileOptsWithService(),
		docMessages(s),
		docMethods(s),
	)

	violations, err := openapi.CheckCompleteness(fds, nil)
	require.NoError(t, err)
	assert.Empty(t, violations, "a fully classifiable surface has no violations")
}

// TestCheckCompleteness_SurfaceParityWithRouteTable tests that the gate gates the
// EXACT REST surface the generator does: gRPC-only methods and files without
// (jsonapi.v1.service) are off-surface for both.
//
// Why this test is important:
//   - If the gate flagged an RPC the generator legitimately skips (a false positive)
//     or missed one the generator routes, the gate and the generator would
//     disagree — the gate would block a build the generator accepts, or vice-versa.
//
// What it tests:
//   - A gRPC-only method (no http binding) in a JSON:API service file is not flagged;
//   - an unclassifiable POST in a file WITHOUT (jsonapi.v1.service) is not flagged,
//     and BuildRouteTable emits no routes for that same off-surface file (parity).
func TestCheckCompleteness_SurfaceParityWithRouteTable(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))

	svcFDS := fixtures.BuildRouteFDS(
		t,
		s.FileOptsWithService(),
		[]*descriptorpb.DescriptorProto{
			fixtures.ResourceMessage(s, "Document", "documents", "document", ""),
			fixtures.EmptyMessage("Empty"),
		},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method("GetDocument", "Empty", "Document",
				s.MethodOpts("GET", "/api/v1/documents/{document_id}", false)),
			fixtures.Method(
				"InternalPing",
				"Empty",
				"Empty",
				nil,
			), // gRPC-only → off-surface
		},
	)
	grpcOnly, err := openapi.CheckCompleteness(svcFDS, nil)
	require.NoError(t, err)
	assert.Empty(t, grpcOnly, "a gRPC-only method is not part of the REST surface")

	offFDS := fixtures.BuildRouteFDS(
		t,
		nil, // no (jsonapi.v1.service) file option
		[]*descriptorpb.DescriptorProto{fixtures.EmptyMessage("Empty")},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method("MarkRead", "Empty", "Empty",
				s.MethodOpts("POST", "/api/v1/x/mark-read", false)),
		},
	)
	offSurface, err := openapi.CheckCompleteness(offFDS, nil)
	require.NoError(t, err)
	assert.Empty(
		t,
		offSurface,
		"an RPC in a non-jsonapi.v1.service file is off-surface",
	)

	table, err := openapi.BuildRouteTable(offFDS, nil)
	require.NoError(t, err, "the generator also ignores the off-surface file (parity)")
	assert.Empty(t, table, "generator emits no routes for an off-surface file")
}
