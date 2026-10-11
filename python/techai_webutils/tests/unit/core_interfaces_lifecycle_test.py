"""Tests for the client lifecycle + health Protocols.

Cover ManagedResource / HealthChecker / Client, the Python analog of Go's
``interfaces.Lifecycle`` / ``HealthChecker`` / ``Client``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self

from techai_webutils.core.interfaces import Client, HealthChecker, ManagedResource

if TYPE_CHECKING:
    from types import TracebackType


class _ManagedOnly:
    """A backend that owns an async-context-manager lifecycle but no health probes."""

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        return None


class _HealthOnly:
    """A backend that reports health but owns no context-manager lifecycle."""

    async def liveness(self) -> None:
        return None

    async def readiness(self) -> None:
        return None


class _FullClient(_ManagedOnly, _HealthOnly):
    """A backend that is both a ManagedResource and a HealthChecker."""


class TestLifecycleProtocols:
    """Structural-satisfaction tests for the runtime-checkable lifecycle Protocols."""

    def test_managed_resource_is_structural(self) -> None:
        """Test that any object with __aenter__/__aexit__ satisfies ManagedResource.

        **Why this test is important:**
          - Backends (S3, SQS, lock, DLQ) already implement the async-context-manager
            lifecycle and must satisfy the contract WITHOUT inheriting it, so an
            ``AsyncExitStack`` composition root manages them uniformly.

        **What it tests:**
          - A managed-only object IS a ManagedResource; a health-only object is not.
        """
        assert isinstance(_ManagedOnly(), ManagedResource)
        assert not isinstance(_HealthOnly(), ManagedResource)

    def test_health_checker_is_structural(self) -> None:
        """Test that any object with async liveness/readiness satisfies HealthChecker.

        **Why this test is important:**
          - Kubernetes probes call liveness/readiness; a backend missing either must not
            be mistaken for health-probeable.

        **What it tests:**
          - A health-only object IS a HealthChecker; a managed-only object is not.
        """
        assert isinstance(_HealthOnly(), HealthChecker)
        assert not isinstance(_ManagedOnly(), HealthChecker)

    def test_client_composes_both_halves(self) -> None:
        """Test that Client requires BOTH the lifecycle and the health surface.

        **Why this test is important:**
          - Client is the base contract for a resilient, lifecycle-managed client; a
            backend satisfying only one half must not pass as a full Client.

        **What it tests:**
          - An object with all four methods IS a Client; each half alone is not.
        """
        assert isinstance(_FullClient(), Client)
        assert not isinstance(_ManagedOnly(), Client)
        assert not isinstance(_HealthOnly(), Client)
