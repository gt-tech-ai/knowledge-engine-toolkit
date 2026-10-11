"""gRPC timeout interceptor."""

from __future__ import annotations

from typing import TYPE_CHECKING

import grpc

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class TimeoutInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that applies a default timeout to gRPC calls."""

    def __init__(self, timeout_seconds: float = 30.0) -> None:
        """Store the default deadline (seconds) applied to calls that carry no explicit timeout."""
        self._timeout = timeout_seconds

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept an async unary-unary call and apply a default timeout if none is set."""
        # Apply timeout if not already set (grpc.aio reads the details by attribute — duck-typed).
        if client_call_details.timeout is None:
            new_details = _ClientCallDetails(
                method=client_call_details.method,
                timeout=self._timeout,
                metadata=client_call_details.metadata,
                credentials=client_call_details.credentials,
                wait_for_ready=client_call_details.wait_for_ready,
            )
            return await continuation(new_details, request)
        return await continuation(client_call_details, request)


class _ClientCallDetails:
    """A duck-typed ClientCallDetails carrier overriding the timeout (grpc.aio reads by attribute)."""

    def __init__(
        self,
        method: str,
        timeout: float | None,
        metadata: object,
        credentials: object,
        wait_for_ready: bool | None,  # noqa: FBT001
    ) -> None:
        """Copy the original call details, substituting the overridden timeout."""
        self.method = method
        self.timeout = timeout
        self.metadata = metadata
        self.credentials = credentials
        self.wait_for_ready = wait_for_ready
