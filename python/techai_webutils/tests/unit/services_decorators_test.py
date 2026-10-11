"""Tests for ServiceBuilder decorator chain.

The underlying ``Repository`` is an ``AsyncMock(spec=Repository)`` whose CRUD
methods are wired to a local dict (reproducing the seed/read-back state the
decorator chain flows through), built inline with ``unittest.mock`` -- there is
no shared fakes package.
"""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import (
    ForbiddenError,
    InternalError,
    NotFoundError,
)
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.services.crud_service import BaseCrudService
from techai_webutils.services.decorators.builder import ServiceBuilder


def _repo_mock(*, seed: dict[str, object] | None = None, delay: float = 0.0) -> AsyncMock:
    """Build an ``AsyncMock(spec=Repository)`` backed by an in-memory dict.

    Reproduces the stateful CRUD semantics the ServiceBuilder decorator tests rely
    on -- seed then read back, create then list, delete then not-exists -- so
    behaviour flows through the real ``BaseCrudService`` + decorator chain unchanged.
    ``seed`` pre-populates the backing dict (the old ``FakeRepository.seed`` helper);
    ``delay`` sleeps before every op, driving the timeout decorator.
    """
    data: dict[object, object] = {}
    data.update((seed or {}).items())
    counter = {"n": 0}
    mock = AsyncMock(spec=Repository)

    async def _sleep() -> None:
        """Apply the configured delay before an op touches the dict."""
        if delay:
            await asyncio.sleep(delay)

    async def _get(entity_id: object) -> object:
        await _sleep()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        return data[entity_id]

    async def _list(params: object, page: PageRequest) -> Page[object]:  # noqa: ARG001
        await _sleep()
        items = list(data.values())
        start = (page.page_number - 1) * page.page_size
        end = start + page.page_size
        return Page(
            items=items[start:end],
            total=len(items),
            page_size=page.page_size,
            page_number=page.page_number,
        )

    async def _create(entity: object) -> object:
        await _sleep()
        counter["n"] += 1
        data[str(counter["n"])] = entity
        return entity

    async def _update(entity_id: object, entity: object) -> object:
        await _sleep()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        data[entity_id] = entity
        return entity

    async def _delete(entity_id: object) -> None:
        await _sleep()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        del data[entity_id]

    async def _exists(entity_id: object) -> bool:
        await _sleep()
        return entity_id in data

    mock.get.side_effect = _get
    mock.list.side_effect = _list
    mock.create.side_effect = _create
    mock.update.side_effect = _update
    mock.delete.side_effect = _delete
    mock.exists.side_effect = _exists
    return mock


class TestServiceBuilder:
    """Test suite for ServiceBuilder composing logging, authorization, recovery, and resilience decorators."""

    @pytest.mark.asyncio
    async def test_with_logging(self) -> None:
        """Test that the logging decorator records service operations.

        **Why this test is important:**
          - Service-level logging is critical for tracing business operation flow in production
          - The decorator must capture the correct operation name for log correlation
          - Missing service logs would make business logic failures invisible during incident response

        **What it tests:**
          - Logger captures a 'service.get' message after a get call
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()
        svc = ServiceBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await svc.get("1")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "service.get" in msg_texts

    @pytest.mark.asyncio
    async def test_with_authorization_allowed(self) -> None:
        """Test that authorized requests pass through to the underlying service.

        **Why this test is important:**
          - Authorization is the primary security gate for all service operations
          - Authorized requests must reach the service layer without modification
          - Incorrect pass-through could silently block legitimate operations

        **What it tests:**
          - Entity is returned when the authorization check passes
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = ServiceBuilder(base, "users").with_authorization(allow).build()
        result = await svc.get("1")
        assert result == "entity"

    @pytest.mark.asyncio
    async def test_with_authorization_denied(self) -> None:
        """Test that unauthorized requests are rejected with ForbiddenError.

        **Why this test is important:**
          - Unauthorized access must be blocked before reaching business logic
          - ForbiddenError maps to HTTP 403, informing clients their credentials lack permission
          - Failure to deny would be a security vulnerability allowing unauthorized data access

        **What it tests:**
          - ForbiddenError is raised when the authorization check fails
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def deny(_action: str) -> bool:
            await asyncio.sleep(0)
            return False

        svc = ServiceBuilder(base, "users").with_authorization(deny).build()
        with pytest.raises(ForbiddenError):
            await svc.get("1")

    @pytest.mark.asyncio
    async def test_with_recovery_wraps_unexpected(self) -> None:
        """Test that the recovery decorator wraps unexpected exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions must not crash the service or leak internal details
          - The recovery decorator provides a safety net for unhandled errors in business logic
          - Preserving the original cause enables debugging while presenting a safe error externally

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        repo.get.side_effect = RuntimeError("boom")
        svc = ServiceBuilder(base, "users").build()

        with pytest.raises(InternalError) as exc_info:
            await svc.get("1")
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_chained_decorators(self) -> None:
        """Test that logging, authorization, and recovery decorators compose correctly.

        **Why this test is important:**
          - Production services use multiple decorators simultaneously
          - Decorator ordering affects behavior (auth before business logic, recovery outermost)
          - This integration-style test catches interaction bugs between decorators

        **What it tests:**
          - Entity is returned through the full decorator chain
          - Logger captures at least one message
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = (
            ServiceBuilder(base, "users")
            .with_logging(logger)  # type: ignore[arg-type]
            .with_authorization(allow)
            .build()
        )
        result = await svc.get("1")
        assert result == "entity"
        assert len(logger.mock_calls) > 0


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


class TestServiceResilienceDecorators:
    """Test suite for timeout and recovery resilience decorators on services."""

    @pytest.mark.asyncio
    async def test_with_timeout_passes_on_fast_op(self) -> None:
        """Test that fast operations succeed when timeout is generous.

        **Why this test is important:**
          - The timeout decorator must not interfere with normal-speed service operations
          - A generous timeout should be invisible to the caller
          - Ensures the decorator correctly passes through results without alteration

        **What it tests:**
          - Service get returns the seeded entity when completing before the deadline
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(5.0).build()

        result = await svc.get("1")
        assert result == "entity"

    @pytest.mark.asyncio
    async def test_with_timeout_raises_on_slow_op(self) -> None:
        """Test that slow operations raise AppTimeoutError when they exceed the deadline.

        **Why this test is important:**
          - Unbounded service operations can exhaust thread pools and degrade the entire system
          - The timeout decorator prevents slow business logic from blocking request processing
          - AppTimeoutError is classified separately from other errors for proper client handling

        **What it tests:**
          - Slow repository triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await svc.get("1")

    @pytest.mark.asyncio
    async def test_timeout_with_recovery(self) -> None:
        """Test that timeout and recovery compose correctly.

        **Why this test is important:**
          - Timeout + recovery is a common production decorator combination
          - Recovery must re-raise AppErrors (like TimeoutError) rather than wrapping them
          - This verifies the decorator ordering contract is respected

        **What it tests:**
          - AppTimeoutError propagates through the recovery decorator without being wrapped
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        # Recovery re-raises AppErrors (which TimeoutError is), so it still raises
        with pytest.raises(AppTimeoutError):
            await svc.get("1")


# ---------------------------------------------------------------------------
# Extended coverage: list/create/update/delete on each decorator
# ---------------------------------------------------------------------------


class TestTimeoutServiceDecoratorExtended:
    """Test suite for timeout decorator across all service operations."""

    @pytest.mark.asyncio
    async def test_list_succeeds_within_timeout(self) -> None:
        """Test that list completes successfully when the operation is fast.

        **Why this test is important:**
          - List operations are the most resource-intensive and most likely to hit timeouts
          - The timeout decorator must correctly wrap paginated service calls
          - Verifies that page metadata is preserved through the timeout wrapper

        **What it tests:**
          - Page total matches seeded entity count
          - Page items contain the seeded entity
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(5.0).build()

        from techai_webutils.core.domain_types.types import PageRequest

        page = await svc.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 1
        assert page.items == ["entity"]

    @pytest.mark.asyncio
    async def test_list_raises_on_slow_op(self) -> None:
        """Test that a slow list operation raises AppTimeoutError.

        **Why this test is important:**
          - Slow list queries can exhaust connection pools and degrade system performance
          - The timeout must apply to list operations just as it does to single-entity lookups
          - Ensures paginated queries are not exempt from timeout protection

        **What it tests:**
          - Slow repository triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        from techai_webutils.core.domain_types.types import PageRequest

        with pytest.raises(AppTimeoutError):
            await svc.list({}, PageRequest(page_size=10, page_number=1))

    @pytest.mark.asyncio
    async def test_create_succeeds_within_timeout(self) -> None:
        """Test that create completes within the timeout deadline.

        **Why this test is important:**
          - Create operations must be protected against slow writes or lock contention
          - The timeout decorator must handle write operations as well as reads
          - Ensures new entity creation is not exempt from deadline enforcement

        **What it tests:**
          - Created entity is returned successfully
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(5.0).build()

        result = await svc.create("new-entity")
        assert result == "new-entity"

    @pytest.mark.asyncio
    async def test_create_raises_on_slow_op(self) -> None:
        """Test that a slow create operation raises AppTimeoutError.

        **Why this test is important:**
          - Slow creates can block request threads and cause cascading timeouts
          - The timeout must apply equally to write operations
          - Ensures the service layer enforces deadlines on entity creation

        **What it tests:**
          - Slow repository triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await svc.create("new-entity")

    @pytest.mark.asyncio
    async def test_update_succeeds_within_timeout(self) -> None:
        """Test that update completes within the timeout deadline.

        **Why this test is important:**
          - Update operations may acquire row-level locks and are susceptible to contention
          - Timeout protection prevents updates from blocking indefinitely on locked rows
          - Ensures the updated value is correctly returned through the timeout wrapper

        **What it tests:**
          - Updated entity value is returned successfully
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(5.0).build()

        result = await svc.update("1", "updated")
        assert result == "updated"

    @pytest.mark.asyncio
    async def test_update_raises_on_slow_op(self) -> None:
        """Test that a slow update operation raises AppTimeoutError.

        **Why this test is important:**
          - Slow updates can indicate lock contention that needs immediate attention
          - The timeout must apply to updates to prevent connection pool exhaustion
          - Ensures the service layer enforces deadlines on entity mutations

        **What it tests:**
          - Slow repository triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await svc.update("1", "updated")

    @pytest.mark.asyncio
    async def test_delete_succeeds_within_timeout(self) -> None:
        """Test that delete completes within the timeout deadline.

        **Why this test is important:**
          - Delete operations with cascading foreign keys can be slow under high load
          - Timeout protection ensures deletes do not hold connections indefinitely
          - Verifies the entity is actually removed despite the timeout wrapper

        **What it tests:**
          - Entity no longer exists in the repository after deletion
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(5.0).build()

        await svc.delete("1")
        # Verify it was deleted
        assert not await repo.exists("1")

    @pytest.mark.asyncio
    async def test_delete_raises_on_slow_op(self) -> None:
        """Test that a slow delete operation raises AppTimeoutError.

        **Why this test is important:**
          - Slow deletes can block request threads and cause cascading failures
          - The timeout must apply to void-returning operations equally
          - Ensures the service layer enforces deadlines on entity deletion

        **What it tests:**
          - Slow repository triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        repo = _repo_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        svc = ServiceBuilder(base, "users").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await svc.delete("1")


class TestLoggingServiceDecoratorExtended:
    """Test suite for logging decorator across all service operations."""

    @pytest.mark.asyncio
    async def test_list_logs(self) -> None:
        """Test that the logging decorator logs list operations.

        **Why this test is important:**
          - List operations are the most common source of performance issues
          - Logging must capture list calls for query analysis and debugging
          - Missing list logs would leave pagination issues undiagnosable

        **What it tests:**
          - Logger captures a 'service.list' message
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()
        svc = ServiceBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        from techai_webutils.core.domain_types.types import PageRequest

        await svc.list({}, PageRequest(page_size=10, page_number=1))
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "service.list" in msg_texts

    @pytest.mark.asyncio
    async def test_create_logs(self) -> None:
        """Test that the logging decorator logs create operations with start and completion.

        **Why this test is important:**
          - Create operations represent new data entering the system and must be auditable
          - Both start and completion messages enable measuring operation duration from logs
          - Missing create/created pairs would break log-based latency analysis

        **What it tests:**
          - Logger captures a 'service.create' message
          - Logger captures a 'service.created' completion message
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()
        svc = ServiceBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await svc.create("new-entity")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "service.create" in msg_texts
        assert "service.created" in msg_texts

    @pytest.mark.asyncio
    async def test_update_logs(self) -> None:
        """Test that the logging decorator logs update operations.

        **Why this test is important:**
          - Update operations modify existing data and must be tracked for audit compliance
          - Logging updates enables debugging data corruption by tracing mutation history
          - Missing update logs would leave state changes unaccounted for

        **What it tests:**
          - Logger captures a 'service.update' message
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()
        svc = ServiceBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await svc.update("1", "updated")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "service.update" in msg_texts

    @pytest.mark.asyncio
    async def test_delete_logs(self) -> None:
        """Test that the logging decorator logs delete operations.

        **Why this test is important:**
          - Delete operations are destructive and must be logged for audit and recovery
          - Logging deletes enables forensic analysis of data loss incidents
          - Missing delete logs would make accidental deletions impossible to trace

        **What it tests:**
          - Logger captures a 'service.delete' message
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)
        logger = MagicMock()
        svc = ServiceBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await svc.delete("1")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "service.delete" in msg_texts


class TestAuthServiceDecoratorExtended:
    """Test suite for authorization decorator across all service operations."""

    @pytest.mark.asyncio
    async def test_list_allowed(self) -> None:
        """Test that list succeeds when authorization passes.

        **Why this test is important:**
          - List operations expose potentially sensitive data and must pass authorization
          - Authorized list requests must proceed without modification to results
          - Verifies the auth decorator does not interfere with pagination metadata

        **What it tests:**
          - Page total matches seeded entity count
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = ServiceBuilder(base, "users").with_authorization(allow).build()

        from techai_webutils.core.domain_types.types import PageRequest

        page = await svc.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 1

    @pytest.mark.asyncio
    async def test_list_denied(self) -> None:
        """Test that list raises ForbiddenError when authorization fails.

        **Why this test is important:**
          - Unauthorized list access could expose sensitive data to unprivileged users
          - The auth decorator must block list operations before any data is fetched
          - ForbiddenError maps to HTTP 403, giving clients clear feedback about permissions

        **What it tests:**
          - ForbiddenError is raised before the operation executes
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def deny(_action: str) -> bool:
            await asyncio.sleep(0)
            return False

        svc = ServiceBuilder(base, "users").with_authorization(deny).build()

        from techai_webutils.core.domain_types.types import PageRequest

        with pytest.raises(ForbiddenError):
            await svc.list({}, PageRequest(page_size=10, page_number=1))

    @pytest.mark.asyncio
    async def test_create_allowed(self) -> None:
        """Test that create succeeds when authorization passes.

        **Why this test is important:**
          - Create operations must be gated by authorization to prevent unauthorized data injection
          - Authorized creates must pass through the entity without modification
          - Verifies the auth decorator handles write operations correctly

        **What it tests:**
          - Created entity is returned successfully
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = ServiceBuilder(base, "users").with_authorization(allow).build()

        result = await svc.create("new-entity")
        assert result == "new-entity"

    @pytest.mark.asyncio
    async def test_create_denied(self) -> None:
        """Test that create raises ForbiddenError when authorization fails.

        **Why this test is important:**
          - Unauthorized creates could inject malicious data into the system
          - The auth decorator must block creates before reaching the repository
          - Ensures write operations are equally protected as read operations

        **What it tests:**
          - ForbiddenError is raised before the operation executes
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def deny(_action: str) -> bool:
            await asyncio.sleep(0)
            return False

        svc = ServiceBuilder(base, "users").with_authorization(deny).build()

        with pytest.raises(ForbiddenError):
            await svc.create("new-entity")

    @pytest.mark.asyncio
    async def test_update_allowed(self) -> None:
        """Test that update succeeds when authorization passes.

        **Why this test is important:**
          - Update operations modify existing data and must require proper authorization
          - Authorized updates must pass through the new value without modification
          - Verifies the auth decorator handles mutation operations correctly

        **What it tests:**
          - Updated entity value is returned successfully
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = ServiceBuilder(base, "users").with_authorization(allow).build()

        result = await svc.update("1", "updated")
        assert result == "updated"

    @pytest.mark.asyncio
    async def test_update_denied(self) -> None:
        """Test that update raises ForbiddenError when authorization fails.

        **Why this test is important:**
          - Unauthorized updates could corrupt data owned by other tenants
          - The auth decorator must block updates before reaching the repository
          - Multi-tenant isolation depends on consistent authorization enforcement

        **What it tests:**
          - ForbiddenError is raised before the operation executes
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def deny(_action: str) -> bool:
            await asyncio.sleep(0)
            return False

        svc = ServiceBuilder(base, "users").with_authorization(deny).build()

        with pytest.raises(ForbiddenError):
            await svc.update("1", "updated")

    @pytest.mark.asyncio
    async def test_delete_allowed(self) -> None:
        """Test that delete succeeds when authorization passes.

        **Why this test is important:**
          - Delete operations are destructive and require the highest authorization scrutiny
          - Authorized deletes must actually remove the entity from the repository
          - Verifies the auth decorator does not interfere with void-returning operations

        **What it tests:**
          - Entity no longer exists in the repository after deletion
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def allow(_action: str) -> bool:
            await asyncio.sleep(0)
            return True

        svc = ServiceBuilder(base, "users").with_authorization(allow).build()

        await svc.delete("1")
        assert not await repo.exists("1")

    @pytest.mark.asyncio
    async def test_delete_denied(self) -> None:
        """Test that delete raises ForbiddenError when authorization fails.

        **Why this test is important:**
          - Unauthorized deletes could destroy critical data across tenants
          - The auth decorator must block deletes before reaching the repository
          - This is the most security-critical authorization check

        **What it tests:**
          - ForbiddenError is raised before the operation executes
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        async def deny(_action: str) -> bool:
            await asyncio.sleep(0)
            return False

        svc = ServiceBuilder(base, "users").with_authorization(deny).build()

        with pytest.raises(ForbiddenError):
            await svc.delete("1")


class TestRecoveryServiceDecoratorExtended:
    """Test suite for recovery decorator across all service operations."""

    @pytest.mark.asyncio
    async def test_list_wraps_unexpected(self) -> None:
        """Test that recovery wraps unexpected list exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions in list operations must not crash the service
          - The recovery decorator provides a safety net for unhandled errors
          - Preserving the cause enables debugging while keeping the API response clean

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        repo.list.side_effect = RuntimeError("boom")
        svc = ServiceBuilder(base, "users").build()

        from techai_webutils.core.domain_types.types import PageRequest

        with pytest.raises(InternalError) as exc_info:
            await svc.list({}, PageRequest(page_size=10, page_number=1))
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_create_wraps_unexpected(self) -> None:
        """Test that recovery wraps unexpected create exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions during entity creation must not crash the service
          - The recovery decorator ensures consistent error handling across all operations
          - Preserving the cause enables root cause analysis of creation failures

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        repo = _repo_mock()
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        repo.create.side_effect = RuntimeError("boom")
        svc = ServiceBuilder(base, "users").build()

        with pytest.raises(InternalError) as exc_info:
            await svc.create("new-entity")
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_update_wraps_unexpected(self) -> None:
        """Test that recovery wraps unexpected update exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions during updates must not crash the service
          - The recovery decorator ensures consistent error handling for mutations
          - Preserving the cause enables debugging of update-specific failures

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        repo.update.side_effect = RuntimeError("boom")
        svc = ServiceBuilder(base, "users").build()

        with pytest.raises(InternalError) as exc_info:
            await svc.update("1", "updated")
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_delete_wraps_unexpected(self) -> None:
        """Test that recovery wraps unexpected delete exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions during deletes must not crash the service
          - The recovery decorator ensures consistent error handling for destructive operations
          - Preserving the cause enables debugging of delete-specific failures

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        repo = _repo_mock(seed={"1": "entity"})
        base: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        repo.delete.side_effect = RuntimeError("boom")
        svc = ServiceBuilder(base, "users").build()

        with pytest.raises(InternalError) as exc_info:
            await svc.delete("1")
        assert exc_info.value.cause is not None
