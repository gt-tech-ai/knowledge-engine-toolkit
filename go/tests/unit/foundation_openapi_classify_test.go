package unit_test

// Black-box tests for the openapi codegen package's operation-role classifier
// (openapi.Classify). They drive the pure Classify(Binding) → (Role, error)
// contract through struct-literal Bindings — no FileDescriptorSet, buf, or mocks
// — because the descriptor → Binding extraction is a separate concern (the route
// table's). Path templates follow real google.api.http bindings, so the extractor and
// these fixtures agree by construction.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
)

// TestClassify_DerivesEachRole tests that Classify derives every non-ACTION
// JSON:API role from a binding's (method, path, resource keys) alone.
//
// Why this test is important:
//   - The classifier is the single source of truth the route table and the gate
//     both consume; a mis-derived role would silently ship the wrong wire
//     envelope (e.g. a create emitted as meta-only), the exact drift this
//     removes.
//   - The collection-only CREATE case guards against keying CREATE on single_key
//     alone, which would drop a bulk-create returning a collection resource.
//
// What it tests:
//   - Each (method, path, keys) shape resolves to exactly the expected role with
//     no error: LIST, GET_ONE (trailing {id} and singleton), CREATE (single- and
//     collection-keyed), UPDATE (PUT and PATCH), DELETE.
func TestClassify_DerivesEachRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want openapi.Role
		in   openapi.Binding
	}{
		{
			name: "LIST — GET collection",
			in: openapi.Binding{
				RPC:           "workspace.v1.DocumentService.ListDocuments",
				HTTPMethod:    "GET",
				PathTemplate:  "/api/v1/workspaces/{workspace_id}/documents",
				CollectionKey: "documents",
			},
			want: openapi.RoleList,
		},
		{
			name: "GET_ONE — GET with trailing {id}",
			in: openapi.Binding{
				RPC:          "workspace.v1.DocumentService.GetDocument",
				HTTPMethod:   "GET",
				PathTemplate: "/api/v1/workspaces/{workspace_id}/documents/{document_id}",
				SingleKey:    "document",
			},
			want: openapi.RoleGetOne,
		},
		{
			name: "GET_ONE — singleton (no trailing {id})",
			in: openapi.Binding{
				RPC:          "identity.v1.IdentityService.GetMe",
				HTTPMethod:   "GET",
				PathTemplate: "/api/v1/identity/me",
				SingleKey:    "user",
			},
			want: openapi.RoleGetOne,
		},
		{
			name: "CREATE — POST collection, single-key response",
			in: openapi.Binding{
				RPC:          "workspace.v1.WorkspaceService.CreateWorkspace",
				HTTPMethod:   "POST",
				PathTemplate: "/api/v1/workspaces",
				SingleKey:    "workspace",
			},
			want: openapi.RoleCreate,
		},
		{
			name: "CREATE — POST bulk, collection-key-only response",
			in: openapi.Binding{
				RPC:           "workspace.v1.DocumentService.CreateUploadURLs",
				HTTPMethod:    "POST",
				PathTemplate:  "/api/v1/workspaces/{workspace_id}/documents/upload-urls",
				CollectionKey: "uploadUrls",
			},
			want: openapi.RoleCreate,
		},
		{
			name: "UPDATE — PUT",
			in: openapi.Binding{
				RPC:          "workspace.v1.DocumentService.UpdateDocument",
				HTTPMethod:   "PUT",
				PathTemplate: "/api/v1/workspaces/{workspace_id}/documents/{document_id}",
				SingleKey:    "document",
			},
			want: openapi.RoleUpdate,
		},
		{
			name: "UPDATE — PATCH",
			in: openapi.Binding{
				RPC:          "workspace.v1.WorkspaceService.PatchWorkspace",
				HTTPMethod:   "PATCH",
				PathTemplate: "/api/v1/workspaces/{workspace_id}",
				SingleKey:    "workspace",
			},
			want: openapi.RoleUpdate,
		},
		{
			name: "DELETE",
			in: openapi.Binding{
				RPC:          "workspace.v1.WorkspaceService.DeleteWorkspace",
				HTTPMethod:   "DELETE",
				PathTemplate: "/api/v1/workspaces/{workspace_id}",
			},
			want: openapi.RoleDelete,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := openapi.Classify(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClassify_PostActionMarkerYieldsAction tests that the (jsonapi.v1.operation)
// action marker forces ACTION even when the response carries a resource shape.
//
// Why this test is important:
//   - A POST returning a resource is the one case derivation cannot resolve (it
//     looks identical to a create); the marker is the only escape, so a create
//     could otherwise be mis-emitted for a semantic action (e.g. "confirm").
//
// What it tests:
//   - POST + resource response + Action:true → ACTION (the marker wins over the
//     CREATE derivation).
func TestClassify_PostActionMarkerYieldsAction(t *testing.T) {
	t.Parallel()

	got, err := openapi.Classify(openapi.Binding{
		RPC:          "workspace.v1.DocumentService.ConfirmDocument",
		HTTPMethod:   "POST",
		PathTemplate: "/api/v1/workspaces/{workspace_id}/documents/{document_id}/confirm",
		SingleKey:    "document",
		Action:       true,
	})
	require.NoError(t, err)
	assert.Equal(t, openapi.RoleAction, got)
}

// TestClassify_UnclassifiableRestRpcErrors tests that a REST RPC the classifier
// cannot resolve is a hard, coded error naming the RPC — never a silent default.
//
// Why this test is important:
//   - "No silent default" is what gives the completeness gate teeth: an
//     unannotated or ambiguous POST must fail generation loudly,
//     not ship an inferred envelope the client cannot predict.
//
// What it tests:
//   - A POST with neither a resource response nor an action marker → error.
//   - A POST that DOES return a resource on a specific-instance path (trailing
//     {id}) but is unmarked → error (ambiguous create-vs-instance-action).
//   - A GET carrying no resource keys → error (no resource to read).
//   - An unhandled HTTP method → error (no role to assign).
//   - Every error is apperr.CodeInvalidInput and contains the RPC full name.
func TestClassify_UnclassifiableRestRpcErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   openapi.Binding
	}{
		{
			name: "POST, no resource response, no marker",
			in: openapi.Binding{
				RPC:          "notification.v1.NotificationService.MarkRead",
				HTTPMethod:   "POST",
				PathTemplate: "/api/v1/notifications/mark-read",
			},
		},
		{
			name: "POST, resource response on trailing {id}, no marker",
			in: openapi.Binding{
				RPC:          "workspace.v1.DocumentService.ReplaceDocument",
				HTTPMethod:   "POST",
				PathTemplate: "/api/v1/workspaces/{workspace_id}/documents/{document_id}",
				SingleKey:    "document",
			},
		},
		{
			name: "GET, no resource keys, no trailing {id}",
			in: openapi.Binding{
				RPC:          "workspace.v1.DocumentService.Ping",
				HTTPMethod:   "GET",
				PathTemplate: "/api/v1/workspaces/{workspace_id}/ping",
			},
		},
		{
			name: "unhandled HTTP method",
			in: openapi.Binding{
				RPC:          "workspace.v1.DocumentService.HeadDocument",
				HTTPMethod:   "HEAD",
				PathTemplate: "/api/v1/workspaces/{workspace_id}/documents/{document_id}",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := openapi.Classify(tc.in)
			require.Error(t, err)
			assert.True(
				t,
				apperr.Is(err, apperr.CodeInvalidInput),
				"want CodeInvalidInput, got %v",
				err,
			)
			assert.Contains(t, err.Error(), tc.in.RPC)
		})
	}
}
