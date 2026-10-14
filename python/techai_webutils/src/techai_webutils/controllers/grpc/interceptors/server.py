"""Runs a gRPC server's RPCs through the controller tier (mirrors Go ``transport/grpc/interceptors``).

``server_interceptors`` lays the tier out as ``grpc.aio`` server interceptors, with gRPC-native
tracing, metrics and logging standing in for the tier's own.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, override

import grpc

from techai_webutils.controllers.grpc.errors import safe_status, to_grpc_status
from techai_webutils.controllers.grpc.interceptors.behavior import rebuild
from techai_webutils.controllers.grpc.interceptors.observe import (
    LoggingServerInterceptor,
    MetricsServerInterceptor,
    TracingServerInterceptor,
)
from techai_webutils.controllers.interceptors import lay_out
from techai_webutils.core.errors.errors import AppError
from techai_webutils.foundation.decorate import Layer, exec_op

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.controllers.grpc.interceptors.behavior import (
        Behavior,
        Context,
        Continuation,
    )
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.foundation.decorate import Deps, OpMiddleware


@dataclass(frozen=True, slots=True)
class Edge:
    """The server's own interceptors around the controller tier (Go ``connect`` ``Edge``)."""

    authn: Sequence[grpc.aio.ServerInterceptor] = ()
    """Authenticate the caller (e.g. ``service_auth``, ``AuthServerInterceptor``) just before
    the tier's auth layer, so it authorizes an authenticated principal."""

    inner: Sequence[grpc.aio.ServerInterceptor] = ()
    """Run nearest the handler, after every tier layer (e.g. ``ValidatingServerInterceptor``)."""


def server_interceptors(
    name: str, deps: Deps, edge: Edge | None = None
) -> list[grpc.aio.ServerInterceptor]:
    """Return the interceptors that run every RPC through the controller tier as ``name``.

    The layers are switched and tuned by the decoration config's ``controllers`` tier:

    - Tracing continues the caller's trace in a server span (``TracingServerInterceptor``),
      metrics count requests by status code (``MetricsServerInterceptor``), and logging writes an
      access line per RPC (``LoggingServerInterceptor``).
    - Recovery, the rate limit, the shedding bulkhead, auth and the timeout run the tier's own
      middleware, the operation named by the method (``pkg.Service/Method``); a stream runs
      without the timeout.
    - ``edge.authn`` authenticate the caller just before the auth layer; ``edge.inner`` run last.

    A handler's own gRPC status reaches the caller unchanged; a coded error the handler raised
    becomes the status of its code (``to_grpc_status``), and a rejection the tier produced itself
    (a shed request, a denial, a timeout) the status of its code carrying only that code's
    client-safe message (``safe_status``, Go ``wire.SanitizeGRPC``).
    """
    edge = edge or Edge()
    obs = deps.observability

    def native(layer: Layer) -> list[grpc.aio.ServerInterceptor] | None:
        match layer:
            case Layer.TRACING:
                return [] if obs.tracer is None else [TracingServerInterceptor(name)]
            case Layer.METRICS:
                return (
                    [] if obs.metrics is None else [MetricsServerInterceptor(obs.metrics)]
                )
            case Layer.LOGGING:
                return (
                    [] if obs.logger is None else [LoggingServerInterceptor(obs.logger)]
                )
            case _:
                return None

    def adapt(unary: OpMiddleware, stream: OpMiddleware) -> grpc.aio.ServerInterceptor:
        return _TierServerInterceptor(unary, stream, obs.logger)

    return [*lay_out(name, deps, native, adapt, edge.authn), *edge.inner]


class _TierServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Runs each RPC through a run of the controller tier's middleware."""

    def __init__(
        self, unary: OpMiddleware, stream: OpMiddleware, logger: Logger | None
    ) -> None:
        """Bind the run's middleware for a unary call and for a streamed response."""
        self._unary = unary
        self._stream = stream
        self._logger = logger

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler in the middleware matching its response shape."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        op = (handler_call_details.method or "unknown").lstrip("/")
        logger = self._logger

        def wrap(behavior: Behavior, streaming: bool) -> Behavior:  # noqa: FBT001
            mw = self._stream if streaming else self._unary

            async def run(request: object, context: Context) -> object:
                own: list[grpc.aio.BaseError] = []
                raised: list[Exception] = []

                async def call() -> object:
                    try:
                        return await behavior(request, context)
                    except grpc.aio.BaseError as exc:
                        own.append(exc)
                        raise
                    except Exception as exc:
                        raised.append(exc)
                        raise

                try:
                    return await exec_op(mw, op, call)
                except grpc.aio.BaseError:
                    raise
                except AppError as exc:
                    if own:
                        raise own[0] from None
                    status = to_grpc_status(exc, logger) if raised else safe_status(exc)
                    await context.abort(*status)
                    raise  # pragma: no cover - abort raises

            return run

        return rebuild(handler, wrap)
