"""Runs a gRPC channel's calls through the client tier (mirrors Go ``clients/rpc/grpc/interceptors``).

``client_interceptors`` returns the ``grpc.aio`` client interceptors a channel is built with, so an
RPC client is decorated like every other client — its layers switched and tuned by the clients
tier of the decoration config — and carries the trace context and the service token on every call.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

import grpc
from grpc.aio import (
    AioRpcError,
    ClientCallDetails,
    Metadata,
    UnaryStreamCall,
    UnaryUnaryCall,
)
from opentelemetry.propagate import inject

from techai_webutils.core.errors import AppError
from techai_webutils.foundation.resilience.grpc_boundary import grpc_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from techai_webutils.clients.client import ClientBase

_AUTHORIZATION_KEY = "authorization"
"""The outgoing metadata key the service token rides on."""

_STATUS_BY_VALUE = {code.value[0]: code for code in grpc.StatusCode}
"""Maps a numeric gRPC status to its ``grpc.StatusCode``."""


def client_interceptors(
    base: ClientBase, service_token: str = ""
) -> list[grpc.aio.ClientInterceptor]:
    """Return the interceptors that run every call on a channel through ``base``, the client tier.

    - A unary call runs as the operation named by its method (``pkg.Service/Method``), retried
      when the tier retries; each attempt gets the tier's deadline, span, metrics and log line.
    - A server-streaming call only carries the outgoing metadata: ``grpc.aio`` opens a stream
      lazily, so there is no open to decorate.

    Every attempt carries the active W3C trace context (so the server continues this trace) and,
    when ``service_token`` is set, ``authorization: Bearer <token>``. The ``AioRpcError`` a server
    returns is coded for the tier (``grpc_error_to_app_error``) and handed back to the caller
    unchanged; a failure the tier produced itself (an open breaker, a full bulkhead, a timeout)
    becomes an ``AioRpcError`` of its code.
    """
    return [_UnaryClient(base, service_token), _StreamClient(service_token)]


def operation(method: str | bytes) -> str:
    """Name a call's operation by its method without the leading slash."""
    name = method.decode() if isinstance(method, bytes) else method
    return name.lstrip("/")


def outgoing(details: ClientCallDetails, token: str) -> ClientCallDetails:
    """Return ``details`` carrying the active trace context and the service token in its metadata.

    The token is not added when it is empty or an ``authorization`` value is already present.
    """
    metadata = Metadata(*(details.metadata or ()))
    carrier: dict[str, str] = {}
    inject(carrier)
    for key, value in carrier.items():
        metadata[key] = value
    if token and _AUTHORIZATION_KEY not in metadata:
        metadata.add(_AUTHORIZATION_KEY, f"Bearer {token}")
    return ClientCallDetails(
        details.method,
        details.timeout,
        metadata,
        details.credentials,
        details.wait_for_ready,
    )


def to_rpc_error(err: AppError) -> AioRpcError:
    """Return the ``AioRpcError`` the caller sees for a coded failure.

    A server's own status (the error's cause) is returned unchanged; a failure the tier produced
    becomes the status of its code.
    """
    if isinstance(err.cause, AioRpcError):
        return err.cause
    code = _STATUS_BY_VALUE.get(err.grpc_status, grpc.StatusCode.INTERNAL)
    return AioRpcError(code, Metadata(), Metadata(), details=err.message)


class _UnaryClient(grpc.aio.UnaryUnaryClientInterceptor):
    """Runs each unary call through the client tier."""

    def __init__(self, base: ClientBase, token: str) -> None:
        """Bind the client's decoration and the service token."""
        self._base = base
        self._token = token

    @override
    async def intercept_unary_unary[TRequest, TResponse](
        self,
        continuation: Callable[
            [ClientCallDetails, TRequest], Awaitable[UnaryUnaryCall[TRequest, TResponse]]
        ],
        client_call_details: ClientCallDetails,
        request: TRequest,
    ) -> TResponse | UnaryUnaryCall[TRequest, TResponse]:
        """Run the call through the tier, awaiting each attempt inside it."""

        async def attempt() -> UnaryUnaryCall[TRequest, TResponse]:
            call = await continuation(outgoing(client_call_details, self._token), request)
            try:
                await call
            except AioRpcError as exc:
                raise grpc_error_to_app_error(exc) from exc
            return call

        try:
            return await self._base.exec(operation(client_call_details.method), attempt)
        except AppError as exc:
            raise to_rpc_error(exc) from exc


class _StreamClient(grpc.aio.UnaryStreamClientInterceptor):
    """Carries the outgoing metadata on each server-streaming call."""

    def __init__(self, token: str) -> None:
        """Bind the service token."""
        self._token = token

    @override
    async def intercept_unary_stream[TRequest, TResponse](
        self,
        continuation: Callable[
            [ClientCallDetails, TRequest], Awaitable[UnaryStreamCall[TRequest, TResponse]]
        ],
        client_call_details: ClientCallDetails,
        request: TRequest,
    ) -> AsyncIterator[TResponse] | UnaryStreamCall[TRequest, TResponse]:
        """Open the stream with the trace context and service token in its metadata."""
        return await continuation(outgoing(client_call_details, self._token), request)
