"""Tests for the shared async-resource lifecycle mixins (NoOpAsyncResource + DelegatingAsyncResource)."""

from unittest.mock import AsyncMock

import pytest

from techai_webutils.core.interfaces.lifecycle import ManagedResource
from techai_webutils.foundation.lifecycle import (
    DelegatingAsyncResource,
    NoOpAsyncResource,
)


class _Backend(NoOpAsyncResource):
    """A stateless backend that mixes in the no-op lifecycle."""


class _Decorator(DelegatingAsyncResource[ManagedResource]):
    """A decorator that mixes in the delegating lifecycle and binds a wrapped inner resource."""

    def __init__(self, inner: ManagedResource) -> None:
        self._inner = inner


class TestNoOpAsyncResource:
    """Behaviour of the shared no-op async-resource mixin the stub/injected backends use."""

    def test_mixin_satisfies_managed_resource(self) -> None:
        """Test that a class mixing in NoOpAsyncResource satisfies ManagedResource.

        **Why this test is important:**
          - The stub / injected-dependency backends (LLM stub, qdrant, ollama, …) rely on this
            mixin to satisfy the ManagedResource contract so an AsyncExitStack manages them
            uniformly; if it stopped satisfying the protocol, every such backend would silently
            drop out of lifecycle management.

        **What it tests:**
          - An object mixing in NoOpAsyncResource is an instance of ManagedResource.
        """
        assert isinstance(_Backend(), ManagedResource)

    @pytest.mark.asyncio
    async def test_aenter_returns_self_and_aexit_is_noop(self) -> None:
        """Test that __aenter__ returns self and __aexit__ is a non-suppressing no-op.

        **Why this test is important:**
          - ``async with backend`` must yield the backend itself (not None, the bug you get from
            an unimplemented protocol stub), and __aexit__ must not swallow exceptions — otherwise
            errors inside the ``async with`` body would be silently hidden.

        **What it tests:**
          - ``async with`` binds the same instance; __aexit__ returns None (does not suppress).
        """
        backend = _Backend()
        async with backend as entered:
            assert entered is backend
        assert await backend.__aexit__(None, None, None) is None


class TestDelegatingAsyncResource:
    """Behaviour of the delegating mixin the poll/retry/filter decorators use to compose lifecycle."""

    def test_mixin_satisfies_managed_resource(self) -> None:
        """Test that a decorator mixing in DelegatingAsyncResource satisfies ManagedResource.

        **Why this test is important:**
          - The KB/retrieval decorators (PollingIngestor, RetryingIngestor, FilteringRetrievalEngine)
            rely on this mixin to satisfy the ManagedResource contract of the interface they wrap; if
            it stopped satisfying the protocol they would become abstract and could not be built.

        **What it tests:**
          - A decorator mixing in DelegatingAsyncResource is an instance of ManagedResource.
        """
        assert isinstance(_Decorator(AsyncMock()), ManagedResource)

    @pytest.mark.asyncio
    async def test_enter_and_exit_delegate_to_wrapped_inner(self) -> None:
        """Test that enter/exit propagate to the wrapped resource and pass through its suppression.

        **Why this test is important:**
          - Lifecycle must compose transparently through a decorator: ``async with decorator`` has to
            open (and close) the wrapped chain down to the connection-owning backend, and must not
            swallow an exception the inner chose to propagate — otherwise decorating a client would
            silently drop its resource management or hide errors.

        **What it tests:**
          - ``async with`` enters the inner and binds the decorator itself; exit exits the inner and
            returns the inner's suppression decision.
        """
        inner = AsyncMock()
        inner.__aexit__.return_value = True  # inner elects to suppress
        decorator = _Decorator(inner)
        async with decorator as entered:
            assert entered is decorator
            inner.__aenter__.assert_awaited_once()
        inner.__aexit__.assert_awaited_once()
        assert await decorator.__aexit__(ValueError, ValueError("x"), None) is True
