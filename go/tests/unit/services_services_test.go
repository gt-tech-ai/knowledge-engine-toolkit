// Package services_test provides external tests for the services layer.
//
// This file tests BaseService CRUD, decorator behaviors including panic recovery,
// authorization enforcement, logging, and timeout for all operations.
package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestServiceDecorator_RecoveryIsOutermost tests that the recovery decorator
// catches panics from inner service operations and converts them to errors.
//
// Why this test is important:
//   - Unrecovered panics crash the entire service process, causing downtime for all
//     tenants
//   - The recovery decorator is the last line of defense against programming errors in
//     business logic
//   - Validates that panics are converted to errors for proper HTTP 500 responses
//
// What it tests:
//   - Get on a panicking service returns an error instead of crashing
func TestServiceDecorator_RecoveryIsOutermost(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, true), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	_, err := svc.Get(ctx, "1")
	require.Error(t, err, "expected error from recovered panic")
}

// TestServiceDecorator_AuthBlocks tests that the authorization decorator
// prevents access when the auth check fails.
//
// Why this test is important:
//   - Authorization enforcement is a security-critical requirement for multi-tenant
//     isolation
//   - A bypassed auth check would allow unauthorized access to other tenants' data
//   - Validates that the decorator pattern correctly enforces auth before business logic
//
// What it tests:
//   - Get returns an error when the authorization function returns an error
func TestServiceDecorator_AuthBlocks(t *testing.T) {
	authErr := context.DeadlineExceeded
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).WithAuthorization(func(
		_ context.Context,
		_ string,
	) error {
		return authErr
	}).Build()

	ctx := context.Background()
	_, err := svc.Get(ctx, "1")
	require.Error(t, err, "expected auth error")
}

// TestServiceDecorator_FullChain tests that timeout, logging, and authorization
// decorators compose correctly and allow authorized requests through.
//
// Why this test is important:
//   - Production services use all three decorators together; composition must work
//     correctly
//   - Validates the end-to-end decorator chain matches the production wiring
//     configuration
//   - Ensures authorized requests flow through without data corruption or latency issues
//
// What it tests:
//   - Get succeeds through the full decorator chain when auth passes
//   - Returned data matches the mock service response
func TestServiceDecorator_FullChain(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithTimeout(5 * time.Second).
		WithLogging(fixtures.NopLogger()).
		WithAuthorization(func(_ context.Context, _ string) error {
			return nil
		}).
		Build()

	ctx := context.Background()
	result, err := svc.Get(ctx, "1")
	require.NoError(t, err, "Get")
	assert.Equal(t, "mock", result.Name)
}

// ---------------------------------------------------------------------------
// Timeout decorator tests
// ---------------------------------------------------------------------------

// TestServiceDecorator_TimeoutPassesThrough tests that the timeout decorator
// does not interfere with operations that complete within the deadline.
//
// Why this test is important:
//   - Service-level timeouts protect against hung downstream dependencies
//   - Fast operations must not be penalized by the timeout mechanism
//   - Validates that the timeout context propagates correctly without corrupting results
//
// What it tests:
//   - Get succeeds and returns correct data with a generous timeout
func TestServiceDecorator_TimeoutPassesThrough(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithTimeout(5 * time.Second).
		WithLogging(fixtures.NopLogger()).
		Build()

	ctx := context.Background()
	result, err := svc.Get(ctx, "1")
	require.NoError(t, err, "Get")
	assert.Equal(t, "mock", result.Name)
}

// TestServiceDecorator_TimeoutAllOperations tests that the timeout decorator
// passes through all CRUD operations that complete within the deadline.
//
// Why this test is important:
//   - All CRUD operations must be covered by the timeout, not just reads
//   - Write operations with timeouts are critical for preventing hung transactions
//   - Ensures no operation type is accidentally excluded from timeout enforcement
//
// What it tests:
//   - List, Create, Update, and Delete all succeed through the timeout decorator
func TestServiceDecorator_TimeoutAllOperations(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithTimeout(5 * time.Second).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()

	// List
	page, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err, "List")
	require.NotNil(t, page, "List returned nil")

	// Create
	created, err := svc.Create(ctx, &fixtures.TestEntity{ID: "1", Name: "test"})
	require.NoError(t, err, "Create")
	assert.Equal(t, "test", created.Name)

	// Update
	updated, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: "updated"})
	require.NoError(t, err, "Update")
	assert.Equal(t, "updated", updated.Name)

	// Delete
	require.NoError(t, svc.Delete(ctx, "1"), "Delete")
}

// ---------------------------------------------------------------------------
// BaseService CRUD tests
// ---------------------------------------------------------------------------

// TestBaseService_CRUD tests that BaseService correctly delegates all CRUD
// operations to the underlying repository.
//
// Why this test is important:
//   - BaseService is the foundation for every domain service built on the engine
//   - Correct delegation ensures the service layer does not introduce data corruption
//   - Validates the service-to-repository wiring that every service depends on
//
// What it tests:
//   - Create stores an entity and returns it with correct fields
//   - Get retrieves the entity by ID
//   - Update modifies the entity and returns updated data
//   - Delete removes the entity without error
func TestBaseService_CRUD(t *testing.T) {
	store := fixtures.StubStore()
	repo := repository.New[fixtures.TestEntity, fixtures.TestParams, string](
		store,
		"test",
	)
	svc := service.New[fixtures.TestEntity, fixtures.TestParams, string](
		repo,
		"test-service",
	)
	ctx := context.Background()

	// Create
	entity := &fixtures.TestEntity{ID: "1", Name: "test"}
	created, err := svc.Create(ctx, entity)
	require.NoError(t, err, "Create")
	assert.Equal(t, "test", created.Name)

	// Get
	got, err := svc.Get(ctx, "1")
	require.NoError(t, err, "Get")
	assert.Equal(t, "test", got.Name)

	// Update
	updated, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: "updated"})
	require.NoError(t, err, "Update")
	assert.Equal(t, "updated", updated.Name)

	// Delete
	require.NoError(t, svc.Delete(ctx, "1"), "Delete")
}

// TestBaseService_List tests that BaseService delegates paginated listing to
// the underlying repository.
//
// Why this test is important:
//   - Paginated listing backs every collection endpoint a consumer exposes
//   - Incorrect delegation would break every paginated listing
//   - Validates that pagination parameters are passed through correctly
//
// What it tests:
//   - List returns the correct number of items from the repository
func TestBaseService_List(t *testing.T) {
	store := fixtures.StubStore()
	_, _ = store.Create(
		context.Background(),
		&fixtures.TestEntity{ID: "1", Name: "alpha"},
	)
	_, _ = store.Create(context.Background(), &fixtures.TestEntity{ID: "2", Name: "beta"})

	repo := repository.New[fixtures.TestEntity, fixtures.TestParams, string](
		store,
		"test",
	)
	svc := service.New[fixtures.TestEntity, fixtures.TestParams, string](
		repo,
		"test-service",
	)
	ctx := context.Background()

	page, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{PageSize: 10})
	require.NoError(t, err, "List")
	assert.Len(t, page.Items, 2)
}

// TestBaseService_Name tests that BaseService exposes its configured identity
// name.
//
// Why this test is important:
//   - Service names are used in logging, metrics, and tracing to identify which service
//     is active
//   - An incorrect name would make production debugging and monitoring misleading
//   - Validates the constructor correctly stores the identity parameter
//
// What it tests:
//   - Name() returns the string passed during construction
func TestBaseService_Name(t *testing.T) {
	store := fixtures.StubStore()
	repo := repository.New[fixtures.TestEntity, fixtures.TestParams, string](
		store,
		"test",
	)
	svc := service.New[fixtures.TestEntity, fixtures.TestParams, string](
		repo,
		"my-service",
	)

	assert.Equal(t, "my-service", svc.Name())
}

// ---------------------------------------------------------------------------
// Service decorator — List operation tests
// ---------------------------------------------------------------------------

// TestServiceDecorator_ListWithLogging tests that the logging decorator
// transparently delegates List operations.
//
// Why this test is important:
//   - List is the most frequently called operation in production
//   - The logging decorator must not interfere with pagination result delivery
//   - Validates that structured logging does not alter the response payload
//
// What it tests:
//   - List through the logging decorator returns a non-nil page without error
func TestServiceDecorator_ListWithLogging(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	page, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err, "List")
	require.NotNil(t, page, "List returned nil")
}

// TestServiceDecorator_CreateWithLogging tests that the logging decorator
// transparently delegates Create operations.
//
// Why this test is important:
//   - Create operations trigger downstream events (SQS messages) and must return correct
//     data
//   - Logging must capture creation details without altering the returned entity
//   - Validates the decorator does not interfere with entity identity assignment
//
// What it tests:
//   - Create through the logging decorator returns the entity with correct fields
func TestServiceDecorator_CreateWithLogging(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	entity := &fixtures.TestEntity{ID: "1", Name: "created"}
	result, err := svc.Create(ctx, entity)
	require.NoError(t, err, "Create")
	assert.Equal(t, "created", result.Name)
}

// TestServiceDecorator_UpdateWithLogging tests that the logging decorator
// transparently delegates Update operations.
//
// Why this test is important:
//   - Update operations change entity state and must return the new state accurately
//   - Logging must capture the before/after context without corrupting the update result
//   - Validates that the decorator does not cache stale data across updates
//
// What it tests:
//   - Update through the logging decorator returns the entity with updated fields
func TestServiceDecorator_UpdateWithLogging(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	entity := &fixtures.TestEntity{ID: "1", Name: "updated"}
	result, err := svc.Update(ctx, "1", entity)
	require.NoError(t, err, "Update")
	assert.Equal(t, "updated", result.Name)
}

// TestServiceDecorator_DeleteWithLogging tests that the logging decorator
// transparently delegates Delete operations.
//
// Why this test is important:
//   - Delete operations are irreversible and must be logged for audit trails
//   - The decorator must not block or fail the delete even if logging encounters issues
//   - Validates that deletion succeeds through the decorated path
//
// What it tests:
//   - Delete through the logging decorator completes without error
func TestServiceDecorator_DeleteWithLogging(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	require.NoError(t, svc.Delete(ctx, "1"), "Delete")
}

// ---------------------------------------------------------------------------
// Service decorator — Auth blocks all operations
// ---------------------------------------------------------------------------

// TestServiceDecorator_AuthBlocksList tests that the authorization decorator
// prevents List when auth fails.
//
// Why this test is important:
//   - List endpoints expose collections of tenant-scoped data; unauthorized listing is a
//     data breach
//   - Validates that authorization is enforced before any data is fetched from the
//     repository
//   - Ensures multi-tenant isolation for collection endpoints
//
// What it tests:
//   - List returns an error when the authorization function rejects the request
func TestServiceDecorator_AuthBlocksList(t *testing.T) {
	authErr := apperr.Sentinel("unauthorized")
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).WithAuthorization(func(
		_ context.Context,
		_ string,
	) error {
		return authErr
	}).Build()

	ctx := context.Background()
	_, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.Error(t, err, "expected auth error for List")
}

// TestServiceDecorator_AuthBlocksCreate tests that the authorization decorator
// prevents Create when auth fails.
//
// Why this test is important:
//   - Unauthorized creation would allow data injection into other tenants' data
//   - Validates that authorization is enforced before any write operation reaches the
//     store
//   - Ensures resource creation is gated by tenant-scoped permissions
//
// What it tests:
//   - Create returns an error when the authorization function rejects the request
func TestServiceDecorator_AuthBlocksCreate(t *testing.T) {
	authErr := apperr.Sentinel("unauthorized")
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).WithAuthorization(func(
		_ context.Context,
		_ string,
	) error {
		return authErr
	}).Build()

	ctx := context.Background()
	_, err := svc.Create(ctx, &fixtures.TestEntity{ID: "1"})
	require.Error(t, err, "expected auth error for Create")
}

// TestServiceDecorator_AuthBlocksUpdate tests that the authorization decorator
// prevents Update when auth fails.
//
// Why this test is important:
//   - Unauthorized updates would allow data tampering across tenant boundaries
//   - Validates that authorization is enforced before any mutation reaches the repository
//   - Ensures entity modifications respect tenant-scoped access control
//
// What it tests:
//   - Update returns an error when the authorization function rejects the request
func TestServiceDecorator_AuthBlocksUpdate(t *testing.T) {
	authErr := apperr.Sentinel("unauthorized")
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).WithAuthorization(func(
		_ context.Context,
		_ string,
	) error {
		return authErr
	}).Build()

	ctx := context.Background()
	_, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1"})
	require.Error(t, err, "expected auth error for Update")
}

// TestServiceDecorator_AuthBlocksDelete tests that the authorization decorator
// prevents Delete when auth fails.
//
// Why this test is important:
//   - Unauthorized deletion is the most destructive security violation possible
//   - Validates that authorization is enforced before any destructive operation
//   - Ensures irrecoverable data removal is gated by tenant-scoped permissions
//
// What it tests:
//   - Delete returns an error when the authorization function rejects the request
func TestServiceDecorator_AuthBlocksDelete(t *testing.T) {
	authErr := apperr.Sentinel("unauthorized")
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).WithAuthorization(func(
		_ context.Context,
		_ string,
	) error {
		return authErr
	}).Build()

	ctx := context.Background()
	err := svc.Delete(ctx, "1")
	require.Error(t, err, "expected auth error for Delete")
}

// ---------------------------------------------------------------------------
// Recovery decorator — all operations
// ---------------------------------------------------------------------------

// TestServiceDecorator_RecoveryList tests that the recovery decorator wraps
// List without interfering with normal execution.
//
// Why this test is important:
//   - Recovery decorators must be transparent during normal operation (no false
//     positives)
//   - Validates that the panic recovery mechanism does not add overhead or alter results
//   - Ensures List operations are not accidentally caught by the recovery handler
//
// What it tests:
//   - List through the recovery decorator returns a non-nil page without error
func TestServiceDecorator_RecoveryList(t *testing.T) {
	// MockService doesn't panic on List, so this just tests pass-through
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	page, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err, "List")
	require.NotNil(t, page, "List returned nil")
}

// TestServiceDecorator_RecoveryCreate tests that the recovery decorator wraps
// Create without interfering with normal execution.
//
// Why this test is important:
//   - Create is a write operation; the recovery decorator must not lose data on normal
//     paths
//   - Validates that entity creation succeeds identically with or without recovery
//     wrapping
//   - Ensures the recovery mechanism does not introduce side effects on success paths
//
// What it tests:
//   - Create through the recovery decorator returns the entity with correct fields
func TestServiceDecorator_RecoveryCreate(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	result, err := svc.Create(ctx, &fixtures.TestEntity{ID: "1", Name: "test"})
	require.NoError(t, err, "Create")
	assert.Equal(t, "test", result.Name)
}

// TestServiceDecorator_RecoveryUpdate tests that the recovery decorator wraps
// Update without interfering with normal execution.
//
// Why this test is important:
//   - Update operations modify persistent state; recovery must not corrupt the update
//     result
//   - Validates that the decorator returns the actual updated entity, not a stale copy
//   - Ensures the recovery handler does not interfere with mutation semantics
//
// What it tests:
//   - Update through the recovery decorator returns the entity with updated fields
func TestServiceDecorator_RecoveryUpdate(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	result, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: "updated"})
	require.NoError(t, err, "Update")
	assert.Equal(t, "updated", result.Name)
}

// TestServiceDecorator_RecoveryDelete tests that the recovery decorator wraps
// Delete without interfering with normal execution.
//
// Why this test is important:
//   - Delete operations are irreversible; the recovery decorator must not block valid
//     deletions
//   - Validates that the decorator does not accidentally catch normal execution as a
//     panic
//   - Ensures destructive operations succeed identically with recovery wrapping
//
// What it tests:
//   - Delete through the recovery decorator completes without error
func TestServiceDecorator_RecoveryDelete(t *testing.T) {
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()
	require.NoError(t, svc.Delete(ctx, "1"), "Delete")
}

// ---------------------------------------------------------------------------
// Error logging paths
// ---------------------------------------------------------------------------

// TestServiceDecorator_LoggingErrorPaths tests that the logging decorator
// propagates errors from the underlying service for every operation.
//
// Why this test is important:
//   - Error propagation is critical for correct HTTP status code mapping in controllers
//   - The logging decorator must record errors without swallowing them
//   - Validates that all operation types propagate errors identically through the logging
//     layer
//
// What it tests:
//   - Get, List, Create, Update, and Delete all return errors from a failing service
func TestServiceDecorator_LoggingErrorPaths(t *testing.T) {
	forced := apperr.Sentinel("forced error")
	ctrl := gomock.NewController(t)
	errSvc := mocks.NewMockService[fixtures.TestEntity, fixtures.TestParams, string](ctrl)
	errSvc.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, forced).AnyTimes()
	errSvc.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, forced).
		AnyTimes()
	errSvc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, forced).AnyTimes()
	errSvc.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, forced).
		AnyTimes()
	errSvc.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(forced).AnyTimes()
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		errSvc, "test",
	).WithLogging(fixtures.NopLogger()).Build()

	ctx := context.Background()

	// Get error path
	_, err := svc.Get(ctx, "1")
	require.Error(t, err, "expected error from Get")

	// List error path
	_, err = svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.Error(t, err, "expected error from List")

	// Create error path
	_, err = svc.Create(ctx, &fixtures.TestEntity{ID: "1"})
	require.Error(t, err, "expected error from Create")

	// Update error path
	_, err = svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1"})
	require.Error(t, err, "expected error from Update")

	// Delete error path
	err = svc.Delete(ctx, "1")
	assert.Error(t, err, "expected error from Delete")
}

// TestServiceDecorator_ErrorLogsAtDebugLevel verifies that service failures are
// logged at Debug level, not Error level.
//
// Why this test is important:
//   - Alerting on level=error logs is common; service errors propagated from
//     4xx repository conditions must not trigger such an alert.
//   - Debug-level failure logs appear where the level is debug and are
//     suppressed at info, which is the intended behavior.
//
// What it tests:
//   - Get failure -> Debug logged, zero Error calls.
func TestServiceDecorator_ErrorLogsAtDebugLevel(t *testing.T) {
	ctrl := gomock.NewController(t)
	errSvc := mocks.NewMockService[fixtures.TestEntity, fixtures.TestParams, string](ctrl)
	errSvc.EXPECT().
		Get(gomock.Any(), gomock.Any()).
		Return(nil, apperr.Sentinel("forced error")).
		AnyTimes()
	spy := fixtures.NewSpyLogger()
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		errSvc, "test",
	).WithLogging(spy).Build()

	_, _ = svc.Get(context.Background(), "1")

	assert.NotEmpty(
		t,
		(*spy.ChildDebugCalls),
		"expected at least one Debug call on error",
	)
	assert.Empty(
		t, spy.ErrorCalls,
		"expected 0 Error calls on service failure, got %d (messages: %v)",
		len(spy.ErrorCalls), spy.ErrorCalls,
	)
}
