"""Tests for resilience interface ABCs."""

import pytest

from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.core.interfaces.byte_cache import ByteCache
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.config_loader import ConfigLoader
from techai_webutils.core.interfaces.crud_service import CrudService, ServiceHooks
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.store import Store


class TestResilienceABCs:
    """Tests for the resilience ABCs."""

    def test_cannot_instantiate_circuit_breaker(self) -> None:
        """Test that CircuitBreakerInterface cannot be instantiated without its methods.

        **Why this test is important:**
          - The circuit breaker is what fails fast when a downstream is unhealthy; an
            instantiable stub whose execute()/__enter__/__exit__ are no-ops would let every
            call through, defeating the protection and allowing cascading failures
          - Forces every breaker implementation to supply real trip/guard logic

        **What it tests:**
          - Instantiating CircuitBreakerInterface directly raises TypeError because execute(),
            __enter__(), and __exit__() are abstract
        """
        with pytest.raises(TypeError):
            CircuitBreakerInterface()  # type: ignore[abstract]

    def test_cannot_instantiate_rate_limiter(self) -> None:
        """Test that the RateLimiter ABC cannot be instantiated without allow/wait.

        **Why this test is important:**
          - RateLimiter enforces the token-bucket cap that protects downstreams from overload;
            an instantiable stub whose allow() always returned True would remove the cap and
            let traffic spikes through unthrottled
          - Forces every limiter to implement real admission control

        **What it tests:**
          - Instantiating RateLimiter directly raises TypeError because allow() and wait() are
            abstract
        """
        with pytest.raises(TypeError):
            RateLimiter()  # type: ignore[abstract]

    def test_cannot_instantiate_bulkhead(self) -> None:
        """Test that the Bulkhead ABC cannot be instantiated without execute/try_execute.

        **Why this test is important:**
          - The bulkhead caps concurrent access so one slow dependency cannot exhaust the whole
            pool; an instantiable stub would skip the concurrency limit, letting a backlog
            consume all capacity and starve other work
          - Forces every bulkhead to implement real slot acquisition

        **What it tests:**
          - Instantiating Bulkhead directly raises TypeError because execute() and try_execute()
            are abstract
        """
        with pytest.raises(TypeError):
            Bulkhead()  # type: ignore[abstract]

    def test_cannot_instantiate_retrier(self) -> None:
        """Test that the Retrier ABC cannot be instantiated without a retry() implementation.

        **Why this test is important:**
          - The retrier is what turns transient downstream blips into eventual success; an
            instantiable stub whose retry() ran the op once would silently disable retries, so
            recoverable failures would surface as user-visible errors
          - Forces every retrier to implement real backoff/retry logic

        **What it tests:**
          - Instantiating Retrier directly raises TypeError because retry() is abstract
        """
        with pytest.raises(TypeError):
            Retrier()  # type: ignore[abstract]


class TestOtherABCs:
    """Tests for the remaining interface ABCs."""

    def test_cannot_instantiate_byte_cache(self) -> None:
        """Test that the ByteCache ABC cannot be instantiated without get/set/delete.

        **Why this test is important:**
          - ByteCache is the raw-bytes cache the metrics/tracing wrappers serialize through; an
            instantiable stub would accept set() and return a perpetual miss on get(), so the
            cache would appear to work while caching nothing
          - Forces every byte-cache backend to implement real storage and retrieval

        **What it tests:**
          - Instantiating ByteCache directly raises TypeError because get(), set(), and delete()
            are abstract
        """
        with pytest.raises(TypeError):
            ByteCache()  # type: ignore[abstract]

    def test_cannot_instantiate_config_loader(self) -> None:
        """Test that the ConfigLoader ABC cannot be instantiated without its accessors.

        **Why this test is important:**
          - ConfigLoader is the read-only contract services use to resolve configuration; an
            instantiable stub returning None for every key would let services boot with empty
            config and fail far from the root cause
          - Forces every loader to implement real typed value access

        **What it tests:**
          - Instantiating ConfigLoader directly raises TypeError because get/get_string/get_int/
            get_bool/unmarshal are abstract
        """
        with pytest.raises(TypeError):
            ConfigLoader()  # type: ignore[abstract]

    def test_cannot_instantiate_store(self) -> None:
        """Test that the Store ABC cannot be instantiated without all CRUD methods.

        **Why this test is important:**
          - Store is the low-level infrastructure data-access boundary (asyncpg/SQLAlchemy); an
            instantiable stub would let a partial backend reach the repo layer and silently drop
            writes or return None reads, corrupting the persistence boundary
          - Forces every store backend to implement the full get/list/create/update/delete/exists
            surface

        **What it tests:**
          - Instantiating Store directly raises TypeError because its CRUD methods are abstract
        """
        with pytest.raises(TypeError):
            Store()  # type: ignore[abstract]

    def test_cannot_instantiate_crud_service(self) -> None:
        """Test that the CrudService ABC cannot be instantiated without its methods.

        **Why this test is important:**
          - CrudService is the business-logic layer that wraps a repository with authorization,
            validation, and event publishing; an instantiable stub would bypass those guards,
            letting writes skip validation and never emit domain events
          - Forces every domain service to implement the full guarded CRUD surface

        **What it tests:**
          - Instantiating CrudService directly raises TypeError because get/list/create/update/
            delete are abstract
        """
        with pytest.raises(TypeError):
            CrudService()  # type: ignore[abstract]


class TestServiceHooks:
    """ServiceHooks has default no-op implementations, so it can be instantiated."""

    @pytest.mark.asyncio
    async def test_default_hooks_are_noop(self) -> None:
        """Test that a ServiceHooks subclass overriding nothing exposes callable no-op hooks.

        **Why this test is important:**
          - ServiceHooks is the optional extension point a CrudService invokes around every
            create/update/delete; the contract is that subclasses override only the hooks they
            need and the rest stay safe no-ops, so a service must be able to call all six on a
            bare subclass without error
          - If a default hook raised or was abstract, every service that did not override all
            six would break on its first operation

        **What it tests:**
          - A subclass that overrides no hooks can be instantiated, and all six lifecycle hooks
            (before/after create/update/delete) complete without raising
        """

        class MyHooks(ServiceHooks[str]):
            pass

        hooks = MyHooks()
        # All default hooks should complete without error
        await hooks.before_create("entity")
        await hooks.after_create("entity")
        await hooks.before_update("entity")
        await hooks.after_update("entity")
        await hooks.before_delete("id-1")
        await hooks.after_delete("id-1")
