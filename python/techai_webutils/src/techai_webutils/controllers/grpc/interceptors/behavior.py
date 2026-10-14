"""Rebuilds a gRPC method handler around a wrapped behavior, for the server interceptors.

Every RPC shape is run as one coroutine: a streaming response is written with
``context.write`` rather than yielded, so a wrapper — a span, a timer, a middleware chain — runs
around the whole call, a stream included, in one task.
"""

from __future__ import annotations

import inspect
from typing import TYPE_CHECKING, cast

import grpc

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

type Context = grpc.aio.ServicerContext[object, object]
"""The servicer context of an RPC."""

type Behavior = Callable[[object, Context], Awaitable[object]]
"""An RPC's behavior as one coroutine: its response for a unary call, None for a stream."""

type Continuation[TRequest, TResponse] = Callable[
    [grpc.HandlerCallDetails],
    Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
]
"""The next interceptor (or the server) resolving a call to its method handler."""

type Wrap = Callable[[Behavior, bool], Behavior]
"""Wraps a behavior; the flag reports whether the response is streamed."""


def rebuild[TRequest, TResponse](
    handler: grpc.RpcMethodHandler[TRequest, TResponse],
    wrap: Wrap,
) -> grpc.RpcMethodHandler[TRequest, TResponse]:
    """Return ``handler`` with its behavior wrapped by ``wrap``, keeping its streaming shape."""
    deserializer = handler.request_deserializer
    serializer = handler.response_serializer
    if handler.response_streaming:
        fn = handler.stream_stream if handler.request_streaming else handler.unary_stream
        behavior = wrap(_as_writer(cast("Callable[..., object]", fn)), True)  # noqa: FBT003
        build = (
            grpc.stream_stream_rpc_method_handler
            if handler.request_streaming
            else grpc.unary_stream_rpc_method_handler
        )
        return build(behavior, deserializer, serializer)  # pyright: ignore[reportArgumentType]
    fn = handler.stream_unary if handler.request_streaming else handler.unary_unary
    behavior = wrap(cast("Behavior", fn), False)  # noqa: FBT003
    build = (
        grpc.stream_unary_rpc_method_handler
        if handler.request_streaming
        else grpc.unary_unary_rpc_method_handler
    )
    return build(behavior, deserializer, serializer)  # pyright: ignore[reportArgumentType]


def _as_writer(fn: Callable[..., object]) -> Behavior:
    """Return a streaming behavior as one coroutine that writes each response it yields."""

    async def run(request: object, context: Context) -> object:
        result = fn(request, context)
        if inspect.isasyncgen(result):
            async for item in cast("AsyncIterator[object]", result):
                await context.write(item)
            return None
        return await cast("Awaitable[object]", result)

    return run
