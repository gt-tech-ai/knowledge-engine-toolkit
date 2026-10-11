"""Tests for RepositoryBuilder decorator chain.

Collaborators are ``unittest.mock`` doubles ``spec``-bound to the real ``core``
interfaces: the underlying ``Store`` is an ``AsyncMock(spec=Store)`` whose CRUD
methods are wired to a local dict (reproducing the seed/read-back state the
decorator chain flows through), the ``Retrier`` an ``AsyncMock(spec=Retrier)``
that runs the op once, and the circuit breaker a ``MagicMock`` usable as a
``with`` context. There is no shared fakes package.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import NotFoundError
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.store import Store
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.repos.decorators.builder import RepositoryBuilder
from techai_webutils.repos.repository import BaseRepository

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _store_mock(
    *,
    seed: dict[str, object] | None = None,
    delay: float = 0.0,
    failing: bool = False,
) -> AsyncMock:
    """Build an ``AsyncMock(spec=Store)`` backed by an in-memory dict.

    Reproduces the stateful CRUD semantics the RepositoryBuilder decorator tests
    rely on -- seed then read back, create then list, delete then not-exists -- so
    behaviour flows through the real ``BaseRepository`` + decorator chain unchanged.
    ``seed`` pre-populates the backing dict (the old ``FakeStore.seed`` helper);
    ``delay`` sleeps before every op (drives the timeout decorator); ``failing``
    makes every op raise ``RuntimeError`` (drives the error/tracing/logging/metrics
    branches).
    """
    data: dict[object, object] = {}
    data.update((seed or {}).items())
    counter = {"n": 0}
    mock = AsyncMock(spec=Store)

    async def _guard() -> None:
        """Apply the configured delay/failure before an op touches the dict."""
        if delay:
            await asyncio.sleep(delay)
        if failing:
            msg = "db down"
            raise RuntimeError(msg)

    async def _get(entity_id: object) -> object:
        await _guard()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        return data[entity_id]

    async def _list(params: object, page: PageRequest) -> Page[object]:  # noqa: ARG001
        await _guard()
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
        await _guard()
        counter["n"] += 1
        data[str(counter["n"])] = entity
        return entity

    async def _update(entity_id: object, entity: object) -> object:
        await _guard()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        data[entity_id] = entity
        return entity

    async def _delete(entity_id: object) -> None:
        await _guard()
        if entity_id not in data:
            msg = f"not found: {entity_id}"
            raise NotFoundError(msg)
        del data[entity_id]

    async def _exists(entity_id: object) -> bool:
        await _guard()
        return entity_id in data

    mock.get.side_effect = _get
    mock.list.side_effect = _list
    mock.create.side_effect = _create
    mock.update.side_effect = _update
    mock.delete.side_effect = _delete
    mock.exists.side_effect = _exists
    return mock


def _retrier_mock() -> AsyncMock:
    """Build an ``AsyncMock(spec=Retrier)`` that runs the wrapped op exactly once.

    The real ``Retrier.retry`` awaits the op and returns its result; this mock
    reproduces that pass-through so the decorated result still reaches the caller,
    while ``retry.assert_awaited_once()`` proves the op was routed through the
    retrier (replacing the old ``FakeRetrier.call_count`` counter).
    """
    mock = AsyncMock(spec=Retrier)

    async def _retry(op: Callable[[], Awaitable[object]]) -> object:
        return await op()

    mock.retry.side_effect = _retry
    return mock


def _cb_mock(
    *, is_open: bool = False, enter_error: BaseException | None = None
) -> MagicMock:
    """Build a ``MagicMock(spec=CircuitBreakerInterface)`` usable as a ``with`` context.

    A closed breaker (default) enters and exits without suppressing exceptions. An
    open breaker raises ``CircuitOpenError`` on entry (the decorator maps it to
    ``UnavailableError``). ``enter_error`` raises an arbitrary exception on entry,
    proving non-circuit errors propagate unchanged.
    """
    mock = MagicMock(spec=CircuitBreakerInterface)
    error = (
        enter_error
        if enter_error is not None
        else (CircuitOpenError() if is_open else None)
    )
    if error is not None:
        mock.__enter__.side_effect = error
    else:
        mock.__enter__.return_value = mock
    mock.__exit__.return_value = False
    return mock


class TestRepositoryBuilder:
    """Test suite for RepositoryBuilder composing logging, metrics, and resilience decorators."""

    @pytest.mark.asyncio
    async def test_build_without_decorators(self) -> None:
        """Test that building without decorators returns a passthrough repository.

        **Why this test is important:**
          - The builder must work correctly even with no decorators applied
          - This is the baseline behavior that all decorator tests build upon
          - Ensures the builder does not introduce unintended side effects when empty

        **What it tests:**
          - get operation returns the seeded entity unmodified
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").build()

        assert await repo.get("1") == "entity"

    @pytest.mark.asyncio
    async def test_with_logging(self) -> None:
        """Test that the logging decorator records repository operations.

        **Why this test is important:**
          - Operation logging is critical for debugging data access issues in production
          - The decorator must capture the correct operation name for log correlation
          - Missing logs would make repository failures invisible during incident response

        **What it tests:**
          - Logger receives a 'repo.get' message after a get call
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await repo.get("1")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "repo.get" in msg_texts

    @pytest.mark.asyncio
    async def test_with_metrics(self) -> None:
        """Test that the metrics decorator records latency for repository operations.

        **Why this test is important:**
          - Repository latency metrics are essential for performance monitoring and SLO tracking
          - The histogram must be called exactly once per operation to avoid inflated metrics
          - Missing metrics would leave data access performance invisible to observability tools

        **What it tests:**
          - Histogram observe is called exactly once after a get call
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        await repo.get("1")
        histogram.observe.assert_called_once()

    @pytest.mark.asyncio
    async def test_chained_decorators(self) -> None:
        """Test that multiple decorators compose and all execute on a single call.

        **Why this test is important:**
          - Production repositories use multiple decorators simultaneously
          - Decorator composition must not interfere with each other or drop calls
          - Verifies the builder's fluent API produces a correct decorator chain

        **What it tests:**
          - Logger captures at least one message
          - Histogram observe is called at least once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        histogram = MagicMock()
        repo = (
            RepositoryBuilder(base, "users")
            .with_metrics(histogram)
            .with_logging(logger)  # type: ignore[arg-type]
            .build()
        )

        await repo.get("1")
        assert len(logger.mock_calls) > 0
        histogram.observe.assert_called()


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


class TestRepositoryResilienceDecorators:
    """Test suite for timeout, retry, and circuit-breaker repository decorators."""

    @pytest.mark.asyncio
    async def test_with_timeout_passes_on_fast_op(self) -> None:
        """Test that fast operations succeed when timeout is generous.

        **Why this test is important:**
          - The timeout decorator must not interfere with normal-speed operations
          - A generous timeout should be invisible to the caller
          - Ensures the decorator correctly passes through results without alteration

        **What it tests:**
          - get operation returns the seeded entity when completing before the deadline
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        result = await repo.get("1")
        assert result == "entity"

    @pytest.mark.asyncio
    async def test_with_timeout_raises_on_slow_op(self) -> None:
        """Test that slow operations raise AppTimeoutError when they exceed the deadline.

        **Why this test is important:**
          - Unbounded database queries can exhaust connection pools and degrade the entire system
          - The timeout decorator prevents slow queries from blocking request threads indefinitely
          - AppTimeoutError is mapped to a specific HTTP response code for client-side handling

        **What it tests:**
          - Slow store triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        store = _store_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await repo.get("1")

    @pytest.mark.asyncio
    async def test_with_retry_passes_through(self) -> None:
        """Test that the retry decorator delegates to the retrier for each operation.

        **Why this test is important:**
          - Transient database failures (network blips, connection resets) are common in production
          - The retry decorator must delegate to the retrier to enable automatic recovery
          - Asserting the retrier is awaited once ensures every operation goes through the retry mechanism

        **What it tests:**
          - Entity is returned successfully through the retrier
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        result = await repo.get("1")
        assert result == "entity"
        retrier.retry.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_with_circuit_breaker_passes_when_closed(self) -> None:
        """Test that operations proceed without error when the circuit breaker is closed.

        **Why this test is important:**
          - A closed circuit breaker means the downstream dependency is healthy
          - Operations must pass through transparently when the circuit is closed
          - Incorrect behavior here would block all repository access even when the DB is healthy

        **What it tests:**
          - Entity is returned successfully when circuit breaker is closed
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await repo.get("1")
        assert result == "entity"

    @pytest.mark.asyncio
    async def test_with_circuit_breaker_raises_when_open(self) -> None:
        """Test that operations raise UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - An open circuit breaker means the database is experiencing sustained failures
          - Fast-failing with UnavailableError prevents cascading failures across services
          - Clients receive immediate feedback instead of waiting for a timeout

        **What it tests:**
          - UnavailableError is raised when circuit breaker is open
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await repo.get("1")

    @pytest.mark.asyncio
    async def test_full_resilience_chain(self) -> None:
        """Test that timeout, retry, CB, metrics, and logging all compose correctly.

        **Why this test is important:**
          - Production repositories use the full decorator chain simultaneously
          - All decorators must compose without interfering with each other
          - This integration-style test catches ordering and interaction bugs between decorators

        **What it tests:**
          - Entity is returned through the full decorator chain
          - Logger captures at least one message
          - Histogram observe is called at least once
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        histogram = MagicMock()
        retrier = _retrier_mock()
        cb = _cb_mock(is_open=False)

        repo = (
            RepositoryBuilder(base, "users")
            .with_circuit_breaker(cb)  # type: ignore[arg-type]
            .with_retry(retrier)  # type: ignore[arg-type]
            .with_timeout(5.0)
            .with_metrics(histogram)
            .with_logging(logger)  # type: ignore[arg-type]
            .build()
        )

        result = await repo.get("1")
        assert result == "entity"
        assert len(logger.mock_calls) > 0
        histogram.observe.assert_called()
        retrier.retry.assert_awaited_once()


# ---------------------------------------------------------------------------
# Extended coverage: list/create/update/delete/exists on each decorator
# ---------------------------------------------------------------------------


class TestTimeoutRepositoryDecoratorExtended:
    """Test suite for timeout decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_list_succeeds_within_timeout(self) -> None:
        """Test that list completes successfully when the operation is fast.

        **Why this test is important:**
          - List operations often involve table scans and are most likely to hit timeouts
          - The timeout decorator must correctly wrap paginated queries
          - Verifies that page metadata is preserved through the timeout wrapper

        **What it tests:**
          - Page total matches seeded entity count
          - Page items contain the seeded entity
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        from techai_webutils.core.domain_types.types import PageRequest

        page = await repo.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 1
        assert page.items == ["entity"]

    @pytest.mark.asyncio
    async def test_list_raises_on_slow_op(self) -> None:
        """Test that a slow list operation raises AppTimeoutError.

        **Why this test is important:**
          - Slow list queries can lock database connections and degrade overall system performance
          - The timeout must apply to list operations just as it does to single-entity lookups
          - Ensures paginated queries are not exempt from timeout protection

        **What it tests:**
          - Slow store triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        store = _store_mock(seed={"1": "entity"}, delay=2.0)
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(0.01).build()

        from techai_webutils.core.domain_types.types import PageRequest

        with pytest.raises(AppTimeoutError):
            await repo.list({}, PageRequest(page_size=10, page_number=1))

    @pytest.mark.asyncio
    async def test_create_succeeds_within_timeout(self) -> None:
        """Test that create completes within the timeout deadline.

        **Why this test is important:**
          - Create operations must be protected against slow writes or lock contention
          - The timeout decorator must handle write operations as well as reads
          - Ensures new entity persistence is not exempt from deadline enforcement

        **What it tests:**
          - Created entity is returned successfully
        """
        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        result = await repo.create("new-entity")
        assert result == "new-entity"

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
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        result = await repo.update("1", "updated")
        assert result == "updated"

    @pytest.mark.asyncio
    async def test_delete_succeeds_within_timeout(self) -> None:
        """Test that delete completes within the timeout deadline.

        **Why this test is important:**
          - Delete operations with cascading foreign keys can be slow under high load
          - Timeout protection ensures deletes do not hold connections indefinitely
          - Verifies the entity is actually removed despite the timeout wrapper

        **What it tests:**
          - Entity no longer exists in the store after deletion
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        await repo.delete("1")
        assert not await store.exists("1")

    @pytest.mark.asyncio
    async def test_exists_succeeds_within_timeout(self) -> None:
        """Test that exists completes within the timeout deadline.

        **Why this test is important:**
          - Exists checks are used in idempotency guards and must be fast
          - Timeout protection prevents existence checks from blocking on table locks
          - Both True and False results must be verified through the timeout wrapper

        **What it tests:**
          - Returns True for an existing entity
          - Returns False for a missing entity
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        repo = RepositoryBuilder(base, "test").with_timeout(5.0).build()

        assert await repo.exists("1") is True
        assert await repo.exists("missing") is False


class TestRetryRepositoryDecoratorExtended:
    """Test suite for retry decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_list_delegates_to_retrier(self) -> None:
        """Test that list is executed through the retrier.

        **Why this test is important:**
          - List queries can fail due to transient connection issues
          - The retrier must wrap list operations to enable automatic recovery
          - Asserting the retrier is awaited once confirms the operation was routed through the retry mechanism

        **What it tests:**
          - Page total matches seeded entity count
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        from techai_webutils.core.domain_types.types import PageRequest

        page = await repo.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 1
        retrier.retry.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_create_delegates_to_retrier(self) -> None:
        """Test that create is executed through the retrier.

        **Why this test is important:**
          - Create operations can fail due to transient network issues
          - The retrier must wrap creates to prevent data loss from transient failures
          - Ensures the retry mechanism applies to write operations, not just reads

        **What it tests:**
          - Created entity is returned successfully
          - Retrier is awaited exactly once
        """
        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        result = await repo.create("new-entity")
        assert result == "new-entity"
        retrier.retry.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_update_delegates_to_retrier(self) -> None:
        """Test that update is executed through the retrier.

        **Why this test is important:**
          - Update failures due to transient issues could leave data in an inconsistent state
          - The retrier must wrap updates to enable automatic recovery from network blips
          - Confirms the retry mechanism does not alter the updated value

        **What it tests:**
          - Updated entity value is returned successfully
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        result = await repo.update("1", "updated")
        assert result == "updated"
        retrier.retry.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_delete_delegates_to_retrier(self) -> None:
        """Test that delete is executed through the retrier.

        **Why this test is important:**
          - Delete failures could leave orphaned data if not retried
          - The retrier must wrap deletes to ensure eventual consistency
          - Confirms the retry mechanism handles void-returning operations correctly

        **What it tests:**
          - Delete completes without error
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        await repo.delete("1")
        retrier.retry.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_exists_delegates_to_retrier(self) -> None:
        """Test that exists is executed through the retrier.

        **Why this test is important:**
          - Exists checks are used in idempotency guards and must not fail on transient errors
          - The retrier must wrap existence checks to prevent false negatives from network issues
          - Confirms the retry mechanism handles boolean-returning operations correctly

        **What it tests:**
          - Returns True for an existing entity
          - Retrier is awaited exactly once
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        retrier = _retrier_mock()
        repo = RepositoryBuilder(base, "test").with_retry(retrier).build()  # type: ignore[arg-type]

        assert await repo.exists("1") is True
        retrier.retry.assert_awaited_once()


class TestCircuitBreakerRepositoryDecoratorExtended:
    """Test suite for circuit breaker decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_list_passes_when_closed(self) -> None:
        """Test that list succeeds when the circuit breaker is closed.

        **Why this test is important:**
          - A closed circuit means the database is healthy and operations should proceed normally
          - List operations must pass through transparently when the circuit is closed
          - Verifies paginated results are not affected by the circuit breaker wrapper

        **What it tests:**
          - Page total matches seeded entity count
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        from techai_webutils.core.domain_types.types import PageRequest

        result = await repo.list({}, PageRequest(page_size=10, page_number=1))
        assert result.total == 1

    @pytest.mark.asyncio
    async def test_list_raises_when_open(self) -> None:
        """Test that list raises UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - An open circuit must block all operations to prevent cascading failures
          - List queries must not bypass the circuit breaker protection
          - Fast-failing prevents connection pool exhaustion during database outages

        **What it tests:**
          - Open circuit breaker triggers UnavailableError on list
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        from techai_webutils.core.domain_types.types import PageRequest

        with pytest.raises(UnavailableError):
            await repo.list({}, PageRequest(page_size=10, page_number=1))

    @pytest.mark.asyncio
    async def test_create_passes_when_closed(self) -> None:
        """Test that create succeeds when the circuit breaker is closed.

        **Why this test is important:**
          - Write operations must proceed normally when the database is healthy
          - The circuit breaker must not block creates when the circuit is closed
          - Verifies the created entity passes through without alteration

        **What it tests:**
          - Created entity is returned successfully
        """
        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await repo.create("new-entity")
        assert result == "new-entity"

    @pytest.mark.asyncio
    async def test_create_raises_when_open(self) -> None:
        """Test that create raises UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - Write operations during an outage would fail anyway and waste resources
          - Fast-failing on creates prevents data inconsistency from partial writes
          - Ensures the circuit breaker protects write paths, not just reads

        **What it tests:**
          - Open circuit breaker triggers UnavailableError on create
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await repo.create("new-entity")

    @pytest.mark.asyncio
    async def test_update_passes_when_closed(self) -> None:
        """Test that update succeeds when the circuit breaker is closed.

        **Why this test is important:**
          - Update operations must proceed normally when the database is healthy
          - The circuit breaker must not block updates when the circuit is closed
          - Verifies the updated value passes through without alteration

        **What it tests:**
          - Updated entity value is returned successfully
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await repo.update("1", "updated")
        assert result == "updated"

    @pytest.mark.asyncio
    async def test_update_raises_when_open(self) -> None:
        """Test that update raises UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - Update attempts during an outage would fail and potentially leave partial state
          - Fast-failing prevents connection pool exhaustion from queued update attempts
          - Ensures mutations are blocked equally with reads during circuit open state

        **What it tests:**
          - Open circuit breaker triggers UnavailableError on update
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await repo.update("1", "updated")

    @pytest.mark.asyncio
    async def test_delete_passes_when_closed(self) -> None:
        """Test that delete succeeds when the circuit breaker is closed.

        **Why this test is important:**
          - Delete operations must proceed normally when the database is healthy
          - The circuit breaker must not block deletes when the circuit is closed
          - Verifies void-returning operations work correctly through the circuit breaker

        **What it tests:**
          - Delete completes without raising an error
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        # Should not raise
        await repo.delete("1")

    @pytest.mark.asyncio
    async def test_delete_raises_when_open(self) -> None:
        """Test that delete raises UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - Delete attempts during an outage would fail and could leave inconsistent state
          - Fast-failing prevents cascading failures from queued delete operations
          - Ensures all mutation types are equally protected by the circuit breaker

        **What it tests:**
          - Open circuit breaker triggers UnavailableError on delete
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await repo.delete("1")

    @pytest.mark.asyncio
    async def test_exists_passes_when_closed(self) -> None:
        """Test that exists succeeds when the circuit breaker is closed.

        **Why this test is important:**
          - Exists checks are lightweight but still depend on database connectivity
          - The circuit breaker must not block existence checks when the database is healthy
          - Verifies boolean-returning operations work correctly through the circuit breaker

        **What it tests:**
          - Returns True for an existing entity
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=False)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await repo.exists("1")
        assert result is True

    @pytest.mark.asyncio
    async def test_exists_raises_when_open(self) -> None:
        """Test that exists raises UnavailableError when the circuit breaker is open.

        **Why this test is important:**
          - Even lightweight existence checks must respect the open circuit breaker
          - Allowing exists calls through would defeat the circuit breaker's purpose
          - Consistent behavior across all operation types simplifies error handling

        **What it tests:**
          - Open circuit breaker triggers UnavailableError on exists
        """
        from techai_webutils.core.errors.errors import UnavailableError

        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(is_open=True)
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await repo.exists("1")

    @pytest.mark.asyncio
    async def test_non_circuit_open_error_re_raises(self) -> None:
        """Test that non-CircuitOpenError exceptions from the circuit breaker propagate.

        **Why this test is important:**
          - The circuit breaker implementation itself may throw unexpected errors
          - These must propagate rather than being swallowed or converted to UnavailableError
          - Ensures debugging visibility when the circuit breaker has internal failures

        **What it tests:**
          - ValueError from a failing circuit breaker is re-raised as-is
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        cb = _cb_mock(enter_error=ValueError("unexpected CB error"))
        repo = RepositoryBuilder(base, "test").with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        with pytest.raises(ValueError, match="unexpected CB error"):
            await repo.get("1")


class TestLoggingRepositoryDecoratorExtended:
    """Test suite for logging decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_list_logs(self) -> None:
        """Test that the logging decorator logs list operations.

        **Why this test is important:**
          - List operations are the most common source of slow queries
          - Logging must capture list calls for query performance analysis
          - Missing list logs would leave pagination issues undiagnosable

        **What it tests:**
          - Logger captures a 'repo.list' message
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        from techai_webutils.core.domain_types.types import PageRequest

        await repo.list({}, PageRequest(page_size=10, page_number=1))
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "repo.list" in msg_texts

    @pytest.mark.asyncio
    async def test_create_logs(self) -> None:
        """Test that the logging decorator logs create operations.

        **Why this test is important:**
          - Create operations represent new data entering the system
          - Logging creates is essential for audit trails and debugging data provenance
          - Missing create logs would make it impossible to trace when entities were persisted

        **What it tests:**
          - Logger captures a 'repo.create' message
        """
        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await repo.create("new-entity")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "repo.create" in msg_texts

    @pytest.mark.asyncio
    async def test_update_logs(self) -> None:
        """Test that the logging decorator logs update operations.

        **Why this test is important:**
          - Update operations modify existing data and must be tracked for audit compliance
          - Logging updates enables debugging data corruption by tracing mutation history
          - Missing update logs would leave state changes unaccounted for

        **What it tests:**
          - Logger captures a 'repo.update' message
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await repo.update("1", "updated")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "repo.update" in msg_texts

    @pytest.mark.asyncio
    async def test_delete_logs(self) -> None:
        """Test that the logging decorator logs delete operations.

        **Why this test is important:**
          - Delete operations are destructive and must be logged for audit and recovery
          - Logging deletes enables forensic analysis of data loss incidents
          - Missing delete logs would make accidental deletions impossible to trace

        **What it tests:**
          - Logger captures a 'repo.delete' message
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        await repo.delete("1")
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "repo.delete" in msg_texts

    @pytest.mark.asyncio
    async def test_exists_delegates(self) -> None:
        """Test that exists works correctly through the logging decorator.

        **Why this test is important:**
          - Exists checks must function correctly even with the logging decorator applied
          - The decorator must not alter the boolean return value
          - Ensures the logging wrapper handles all return types correctly

        **What it tests:**
          - Returns True for an existing entity
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        result = await repo.exists("1")
        assert result is True


class TestMetricsRepositoryDecoratorExtended:
    """Test suite for metrics decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_list_records_latency(self) -> None:
        """Test that list records latency with the correct operation label.

        **Why this test is important:**
          - Per-operation latency metrics enable identifying slow query patterns
          - The 'list' label must be correct for dashboard filtering and alerting
          - Incorrect labels would aggregate different operations, masking performance issues

        **What it tests:**
          - Histogram observe is called once
          - Operation label is 'list'
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        from techai_webutils.core.domain_types.types import PageRequest

        await repo.list({}, PageRequest(page_size=10, page_number=1))
        histogram.observe.assert_called_once()
        call_kwargs = histogram.observe.call_args[1]
        assert call_kwargs["op"] == "list"

    @pytest.mark.asyncio
    async def test_create_records_latency(self) -> None:
        """Test that create records latency with the correct operation label.

        **Why this test is important:**
          - Write latency metrics reveal database contention and index overhead
          - The 'create' label must be correct for isolating insert performance
          - Enables alerting on write degradation separately from read degradation

        **What it tests:**
          - Histogram observe is called once
          - Operation label is 'create'
        """
        store = _store_mock()
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        await repo.create("new-entity")
        histogram.observe.assert_called_once()
        call_kwargs = histogram.observe.call_args[1]
        assert call_kwargs["op"] == "create"

    @pytest.mark.asyncio
    async def test_update_records_latency(self) -> None:
        """Test that update records latency with the correct operation label.

        **Why this test is important:**
          - Update latency metrics reveal lock contention and index maintenance overhead
          - The 'update' label must be correct for isolating mutation performance
          - Enables separate SLO tracking for read vs write operations

        **What it tests:**
          - Histogram observe is called once
          - Operation label is 'update'
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        await repo.update("1", "updated")
        histogram.observe.assert_called_once()
        call_kwargs = histogram.observe.call_args[1]
        assert call_kwargs["op"] == "update"

    @pytest.mark.asyncio
    async def test_delete_records_latency(self) -> None:
        """Test that delete records latency with the correct operation label.

        **Why this test is important:**
          - Delete latency metrics reveal cascading FK overhead and vacuum pressure
          - The 'delete' label must be correct for isolating deletion performance
          - Enables alerting on slow deletes that could indicate table bloat

        **What it tests:**
          - Histogram observe is called once
          - Operation label is 'delete'
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        await repo.delete("1")
        histogram.observe.assert_called_once()
        call_kwargs = histogram.observe.call_args[1]
        assert call_kwargs["op"] == "delete"

    @pytest.mark.asyncio
    async def test_exists_records_latency(self) -> None:
        """Test that exists records latency with the correct operation label.

        **Why this test is important:**
          - Exists checks should be the fastest operation and deviations signal index issues
          - The 'exists' label must be correct for baseline latency comparison
          - Enables detecting when existence checks degrade, often indicating missing indexes

        **What it tests:**
          - Histogram observe is called once
          - Operation label is 'exists'
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        histogram = MagicMock()
        repo = RepositoryBuilder(base, "users").with_metrics(histogram).build()

        await repo.exists("1")
        histogram.observe.assert_called_once()
        call_kwargs = histogram.observe.call_args[1]
        assert call_kwargs["op"] == "exists"


# ---------------------------------------------------------------------------
# Tracing decorator + failure paths (logging error branch, metrics error counter)
# ---------------------------------------------------------------------------


def _page_request():  # noqa: ANN202
    """Return a first-page request (imported here to keep the module import list stable)."""
    from techai_webutils.core.domain_types.types import PageRequest

    return PageRequest(page_size=10, page_number=1)


class TestTracingRepositoryDecorator:
    """Test suite for the tracing decorator across all repository operations."""

    @pytest.mark.asyncio
    async def test_tracing_opens_a_span_per_operation(self) -> None:
        """Every CRUD operation runs inside its own ``repo.<op>`` span.

        **Why this test is important:**
          - Per-operation spans make the repository visible in the trace waterfall;
            a missing span leaves a data-access gap in incident forensics.

        **What it tests:**
          - Each of get/list/create/update/delete/exists opens the matching span.
        """
        store = _store_mock(seed={"1": "entity"})
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(store)
        tracer = MagicMock()
        repo = RepositoryBuilder(base, "users").with_tracing(tracer).build()

        await repo.get("1")
        await repo.list({}, _page_request())
        await repo.create("entity2")
        await repo.update("1", "updated")
        await repo.exists("1")
        await repo.delete("1")

        span_names = {c.args[0] for c in tracer.span.call_args_list}
        assert {
            "repo.get",
            "repo.list",
            "repo.create",
            "repo.update",
            "repo.exists",
            "repo.delete",
        } <= span_names

    @pytest.mark.asyncio
    async def test_tracing_records_error_on_span(self) -> None:
        """A failing operation records the error on its span and re-raises.

        **Why this test is important:**
          - An error span with no recorded error is invisible in trace-based error
            analysis; recording it is what surfaces the failure in the trace backend.

        **What it tests:**
          - A raising inner op calls span.record_error and the exception propagates.
        """
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(
            _store_mock(failing=True)
        )
        tracer = MagicMock()
        span = tracer.span.return_value.__enter__.return_value
        repo = RepositoryBuilder(base, "users").with_tracing(tracer).build()

        with pytest.raises(RuntimeError):
            await repo.get("1")
        span.record_error.assert_called_once()


class TestLoggingRepositoryDecoratorFailurePaths:
    """Test suite for the logging decorator's error branch across mutating operations."""

    @pytest.mark.asyncio
    async def test_all_operations_log_on_failure(self) -> None:
        """Each logged operation logs a ``repo.<op> failed`` record and re-raises.

        **Why this test is important:**
          - The error branch is what makes a repository failure diagnosable; if it
            silently swallowed or failed to log, incidents would have no signal.

        **What it tests:**
          - get/list/create/update/delete each emit their ``failed`` log and re-raise.
        """
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(
            _store_mock(failing=True)
        )
        logger = MagicMock()
        repo = RepositoryBuilder(base, "users").with_logging(logger).build()  # type: ignore[arg-type]

        ops = [
            lambda: repo.get("1"),
            lambda: repo.list({}, _page_request()),
            lambda: repo.create("entity"),
            lambda: repo.update("1", "updated"),
            lambda: repo.delete("1"),
        ]
        for op in ops:
            with pytest.raises(RuntimeError):
                await op()

        failed = {c.args[0] for c in logger.exception.call_args_list}
        assert {
            "repo.get failed",
            "repo.list failed",
            "repo.create failed",
            "repo.update failed",
            "repo.delete failed",
        } <= failed


class TestMetricsRepositoryDecoratorErrorCounter:
    """Test suite for the metrics decorator's error-counter branch."""

    @pytest.mark.asyncio
    async def test_failure_increments_error_counter(self) -> None:
        """A failing operation increments the error counter and still records latency.

        **Why this test is important:**
          - The error counter drives the repository error-rate SLO; a failure that
            didn't increment it would hide data-access outages from alerting.

        **What it tests:**
          - A raising op increments the errors counter and observes duration once.
        """
        base: BaseRepository[str, dict[str, object], str] = BaseRepository(
            _store_mock(failing=True)
        )
        histogram, executions, errors = MagicMock(), MagicMock(), MagicMock()
        repo = (
            RepositoryBuilder(base, "users")
            .with_metrics(histogram, executions, errors)
            .build()
        )

        with pytest.raises(RuntimeError):
            await repo.get("1")
        errors.inc.assert_called_once()
        histogram.observe.assert_called_once()
