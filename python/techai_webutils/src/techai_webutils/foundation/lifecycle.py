"""Shared async-resource lifecycle mixins for backends and decorators.

Two shapes recur when satisfying ``core.interfaces.lifecycle.ManagedResource``:

- ``NoOpAsyncResource`` — a backend that owns no resource (a stub, or one whose collaborators
  — an httpx client, a qdrant client, an embedder+store — are *injected* and owned elsewhere)
  still satisfies ``ManagedResource`` so it registers uniformly with an ``AsyncExitStack``.
- ``DelegatingAsyncResource`` — a decorator/composite that wraps a single ``ManagedResource``
  (``self._inner``) is itself a ManagedResource whose enter/exit open and close the delegate,
  so entering the decorator opens the whole chain down to the connection-owning backend
  (lifecycle composes transparently through the decorator stack).

Both provide the canonical 3-arg ``__aexit__`` signature the connection-owning backends use,
so a backend/decorator mixes one in instead of re-declaring an identical enter/exit.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self

from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from types import TracebackType


class NoOpAsyncResource:
    """Mixin: an async context manager that owns no resource (no-op enter/exit)."""

    async def __aenter__(self) -> Self:
        """Enter the async context; there is no resource to acquire."""
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Exit the async context; there is no resource to release."""


class DelegatingAsyncResource[ResourceT: ManagedResource]:
    """Mixin: an async-context decorator that delegates its lifecycle to the wrapped ``self._inner``.

    A decorator/composite over a single ``ManagedResource`` is itself a ManagedResource whose
    enter/exit propagate to the delegate, so ``async with decorator`` opens (and closes) the whole
    wrapped chain down to the connection-owning backend. Parameterize it with the wrapped resource's
    concrete type (``DelegatingAsyncResource[KnowledgeBaseIngestor]``) so ``self._inner`` keeps that
    type's methods; the concrete decorator binds ``self._inner`` in its ``__init__``. A composite over
    *multiple* resources implements enter/exit explicitly (e.g. via an ``AsyncExitStack``) instead.
    """

    # Bound by the concrete decorator's __init__ (the wrapped resource whose lifecycle we manage).
    _inner: ResourceT
    """The wrapped ``ManagedResource`` whose enter/exit this mixin delegates to."""

    async def __aenter__(self) -> Self:
        """Enter the wrapped resource's async context, then return self."""
        await self._inner.__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit the wrapped resource's async context, propagating its suppression decision."""
        return await self._inner.__aexit__(exc_type, exc_val, exc_tb)
