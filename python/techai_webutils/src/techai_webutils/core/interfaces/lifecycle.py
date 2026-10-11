"""Client lifecycle + health contracts (the Python analog of Go's lifecycle stack).

These mirror Go's ``interfaces.Lifecycle`` / ``HealthChecker`` / ``Client``.
Python's lifecycle idiom is the async context manager managed by
``contextlib.AsyncExitStack`` at a service's composition root: it enters
resources in registration order and exits them in reverse — exactly the role Go's
``foundation/lifecycle.Manager`` plays. These Protocols give that idiom a *named*
contract and add the Kubernetes health-probe surface (liveness/readiness) the
composition root and health endpoints check.

Because these are ``@runtime_checkable`` Protocols, any backend that already
implements ``__aenter__``/``__aexit__`` (the S3, SQS, lock, and DLQ clients do)
structurally satisfies ``ManagedResource`` without inheriting from it.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, Self, runtime_checkable

if TYPE_CHECKING:
    from types import TracebackType


@runtime_checkable
class ManagedResource(Protocol):
    """A client/component whose lifecycle is an async context manager.

    Connection-owning backends acquire their resource in ``__aenter__`` and release it
    in ``__aexit__``, so a composition root manages them uniformly with an
    ``AsyncExitStack`` (enter in order, exit in reverse). It is the Python analog of
    Go's ``interfaces.Lifecycle`` (Start/Stop); ``AsyncExitStack`` is the analog of
    ``foundation/lifecycle.Manager``.
    """

    async def __aenter__(self) -> Self:
        """Enter the async context, acquiring the resource; return self."""
        ...

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit the async context, releasing any owned resource."""
        ...


@runtime_checkable
class HealthChecker(Protocol):
    """Reports a component's health for Kubernetes probes.

    ``liveness`` answers "is it alive?" — a lightweight self-check that does not depend
    on external systems (restart the pod if it raises). ``readiness`` answers "is it
    ready to serve?" — typically verifies connectivity, e.g. a ping (deroute the pod if
    it raises). Both raise on failure and return ``None`` on success. Mirrors Go's
    ``interfaces.HealthChecker``.
    """

    async def liveness(self) -> None:
        """Raise if the component is not alive; return None if it is."""
        ...

    async def readiness(self) -> None:
        """Raise if the component cannot serve traffic; return None if it can."""
        ...


@runtime_checkable
class Client(ManagedResource, HealthChecker, Protocol):
    """The base contract for a resilient, lifecycle-managed client.

    It owns an async resource lifecycle (``ManagedResource``) and answers Kubernetes
    health probes (``HealthChecker``). Mirrors Go's ``interfaces.Client``.
    """
