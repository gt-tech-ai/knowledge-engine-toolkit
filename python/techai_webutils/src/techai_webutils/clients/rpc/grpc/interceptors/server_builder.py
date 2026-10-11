"""Fluent builder for composing async gRPC server interceptors.

Mirrors Go's ``go/clients/transport/connect/interceptors/builder.go`` ServerBuilder. The interceptors are
**async** (``grpc.aio.ServerInterceptor``) so they compose onto the ``grpc.aio`` servers the Python
services run. Ordering: recovery → rate_limit → metrics → tracing → logging → service_auth → auth →
validation (validation is innermost, so it checks the exact request the handler receives, after auth).

Logging convention: the transport (server) logging interceptor is the ONE prod request log — it emits
at ``info``. Every inner layer's logging decorator stays at ``debug`` (dev-only) to avoid the
redundant N-per-request logging.
"""

from __future__ import annotations

import hmac
from time import perf_counter
from typing import TYPE_CHECKING, cast, override

import grpc
from google.protobuf.message import Message

from techai_webutils.clients.rpc.grpc.interceptors.auth import (
    AuthServerInterceptor,
    HeaderClaimMapping,
)
from techai_webutils.clients.rpc.grpc.interceptors.tracing_server import (
    TracingServerInterceptor,
)
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter

    # A gRPC handler's unary behaviour (unary_unary / stream_unary) and streaming behaviour
    # (unary_stream / stream_stream) — the callables the metrics/recovery wrappers reconstruct.
    _UnaryBehavior = Callable[
        [object, grpc.aio.ServicerContext[object, object]], Awaitable[object]
    ]
    _StreamBehavior = Callable[
        [object, grpc.aio.ServicerContext[object, object]], AsyncIterator[object]
    ]

# Recovery logs an uncaught handler exception here (the injected transport logger owns the per-request
# info log; this is the error-level panic signal). build() also warns here about an open server.
_logger = get_logger(__name__)

_BEARER_PREFIX = "Bearer "
"""The scheme prefix of an ``authorization: Bearer <token>`` metadata value."""


def _split_tokens(expected_token: str) -> tuple[str, ...]:
    """Split a ``current[,previous]`` expected token into its non-blank tokens (a rotation window)."""
    return tuple(token for part in expected_token.split(",") if (token := part.strip()))


class ServerInterceptorBuilder:
    """Compose async gRPC server interceptors in a fluent chain.

    Example::

        interceptors = (ServerInterceptorBuilder()
            .with_recovery()
            .with_logging(logger)
            .with_service_auth(token)
            .with_auth(HeaderClaimMapping(user_id="x-sub", tenant_id="x-tenant", roles="x-roles"))
            .build())
        server = grpc.aio.server(interceptors=interceptors)
    """

    def __init__(self) -> None:
        """Start with every interceptor disabled; ``with_*`` methods opt each in."""
        self._auth_headers: HeaderClaimMapping | None = None
        self._service_token: str = ""
        self._service_auth_bypassed: bool = False
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._executions: MetricCounter | None = None
        self._errors: MetricCounter | None = None
        self._tracing_service: str | None = None
        self._rate_limiter: RateLimiter | None = None
        self._recovery: bool = False
        self._validation: bool = False

    def with_recovery(self) -> ServerInterceptorBuilder:
        """Add the recovery interceptor (outermost).

        Recovers an uncaught handler exception into a logged ``INTERNAL`` abort
        (ARCHITECTURE.md#error-codes), instead of leaking gRPC's opaque ``UNKNOWN``. An intentional abort
        and a client-cancellation pass through unchanged.
        """
        self._recovery = True
        return self

    def with_rate_limit(self, limiter: RateLimiter) -> ServerInterceptorBuilder:
        """Add rate limiting interceptor."""
        self._rate_limiter = limiter
        return self

    def with_metrics(
        self,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> ServerInterceptorBuilder:
        """Add the metrics interceptor.

        Records per-RPC duration (``histogram``), execution count, and error count around the
        (unary or streamed) handler run.
        """
        self._histogram = histogram
        self._executions = executions
        self._errors = errors
        return self

    def with_tracing(self, service_name: str) -> ServerInterceptorBuilder:
        """Add the tracing interceptor: continue the inbound W3C trace into a per-RPC SERVER span.

        ``service_name`` is the OTel instrumentation-scope name (e.g. ``"retrieval"``). The span is
        parented on the inbound traceparent, so an upstream trace (gateway → upstream service → this
        service) stays a single trace, and every inner client-stack span shares its trace id.
        """
        self._tracing_service = service_name
        return self

    def with_logging(self, logger: Logger) -> ServerInterceptorBuilder:
        """Add the transport logging interceptor (the prod per-request log, at info)."""
        self._logger = logger
        return self

    def with_service_auth(
        self, expected_token: str, *, allow_unauthenticated: bool = False
    ) -> ServerInterceptorBuilder:
        """Add service-to-service token validation (rejects callers without a valid bearer token).

        ``expected_token`` may carry a comma-separated ``current,previous`` pair so a token rotation has
        a window in which both are accepted (mirroring the Go static validator); blank entries are
        ignored and tokens are compared in constant time. A token with no non-blank entry raises
        ``ValueError``: a blank secret must never silently leave the service open. A local-dev bypass is
        explicit — pass ``allow_unauthenticated=True`` (mirroring the Go builder's ``stub`` flag) and an
        empty token builds without the check (``build`` logs a warning).
        """
        tokens = _split_tokens(expected_token)
        if not tokens and not allow_unauthenticated:
            msg = (
                "service auth requires a non-empty service token "
                "(pass allow_unauthenticated=True for local dev)"
            )
            raise ValueError(msg)
        self._service_token = ",".join(tokens)
        self._service_auth_bypassed = not tokens
        return self

    def with_auth(self, headers: HeaderClaimMapping) -> ServerInterceptorBuilder:
        """Add the auth-claims extraction interceptor, reading the gateway metadata keys in ``headers``.

        The claims are only as trustworthy as the metadata: without ``with_service_auth`` any caller
        that reaches the server can set them, so ``build`` logs a warning in that case.
        """
        self._auth_headers = headers
        return self

    def with_validation(self) -> ServerInterceptorBuilder:
        """Add the protovalidate request-validation interceptor (innermost, right before the handler).

        Mirrors the Go connect ``ValidateInterceptor`` — the Python gRPC servers run the same compiled
        protovalidate rules, so an inbound request that violates a rule is rejected server-side with
        ``INVALID_ARGUMENT`` (ARCHITECTURE.md#error-codes; Go parity) instead of reaching the handler
        unchecked.
        """
        self._validation = True
        return self

    def build(self) -> list[grpc.aio.ServerInterceptor]:
        """Return the composed list of async server interceptors (outermost first).

        Logs a warning when service auth was bypassed (``allow_unauthenticated`` with no token) and when
        auth claims are read without service auth, so an open server is visible in the logs.
        """
        if self._service_auth_bypassed:
            _logger.warning(
                "gRPC service auth bypassed (allow_unauthenticated): every caller is admitted"
            )
        if self._auth_headers is not None and not self._service_token:
            _logger.warning(
                "gRPC auth claims are read from request metadata without service auth; "
                "any caller that reaches the server can set them"
            )
        interceptors: list[grpc.aio.ServerInterceptor] = []

        if self._recovery:
            interceptors.append(_RecoveryServerInterceptor())

        if self._rate_limiter is not None:
            interceptors.append(_RateLimitServerInterceptor(self._rate_limiter))

        if self._histogram is not None:
            interceptors.append(
                _MetricsServerInterceptor(
                    self._histogram, self._executions, self._errors
                ),
            )

        # Tracing sits OUTSIDE logging (recovery → rate_limit → metrics → tracing → logging → auth), so
        # the SERVER span wraps the handler and every inner client-stack span is its child — the whole
        # request is one trace. Without this branch with_tracing() was a silent no-op (the interceptor
        # was never built), leaving each service's inner spans as disconnected roots.
        if self._tracing_service is not None:
            interceptors.append(TracingServerInterceptor(self._tracing_service))

        if self._logger is not None:
            interceptors.append(_LoggingServerInterceptor(self._logger))

        if self._service_token:
            interceptors.append(_ServiceAuthServerInterceptor(self._service_token))

        if self._auth_headers is not None:
            interceptors.append(AuthServerInterceptor(self._auth_headers))

        # Validation is INNERMOST (appended last → closest to the handler): it validates the exact
        # request the authenticated handler receives, after auth has run.
        if self._validation:
            interceptors.append(_ValidatingServerInterceptor())

        return interceptors


class _RecoveryServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Recovers an uncaught handler exception into a coded ``INTERNAL`` abort (ARCHITECTURE.md#error-codes).

    The handler is reconstructed so the wrapper executes (and, for a stream, iterates) the real
    handler, catches an *unexpected* exception, logs it, and aborts ``INTERNAL`` — matching the Go
    recovery interceptor (``codes.Internal``) instead of leaking gRPC's opaque ``UNKNOWN``. An
    intentional abort (``grpc.aio.AbortError``, and any ``grpc.aio.BaseError``) is re-raised unchanged
    so a handler's deliberate status is never rewritten; ``CancelledError`` is a ``BaseException`` (not
    caught by ``except Exception``), so a client disconnect is not turned into an error either.
    """

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler so an unexpected exception becomes a logged INTERNAL abort."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        method = handler_call_details.method or "unknown"

        def wrap_unary(fn: _UnaryBehavior) -> _UnaryBehavior:
            async def _recovered(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> object:
                """Run the unary handler, converting an uncaught panic into an INTERNAL abort."""
                try:
                    return await fn(request, context)
                except grpc.aio.BaseError:
                    raise
                except Exception as exc:
                    _logger.exception(
                        "grpc handler panic recovered", method=method, error=str(exc)
                    )
                    await context.abort(grpc.StatusCode.INTERNAL, "internal server error")
                    return None  # unreachable: abort raises

            return _recovered

        def wrap_stream(fn: _StreamBehavior) -> _StreamBehavior:
            async def _recovered(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> AsyncIterator[object]:
                """Run the streaming handler, turning an uncaught panic mid-stream into an INTERNAL abort."""
                try:
                    async for item in fn(request, context):
                        yield item
                except grpc.aio.BaseError:
                    raise
                except Exception as exc:
                    _logger.exception(
                        "grpc handler panic recovered", method=method, error=str(exc)
                    )
                    await context.abort(grpc.StatusCode.INTERNAL, "internal server error")

            return _recovered

        return _rebuild_handler(handler, wrap_unary, wrap_stream)


class _RateLimitServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Rejects requests when the rate limit is exceeded."""

    def __init__(self, limiter: RateLimiter) -> None:
        """Store the rate limiter consulted before each RPC."""
        self._limiter = limiter

    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Reject the RPC when the limiter is exhausted, else delegate onward."""
        if not self._limiter.allow():
            return None  # gRPC returns UNIMPLEMENTED; a full impl would abort RESOURCE_EXHAUSTED
        return await continuation(handler_call_details)


class _MetricsServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Records the per-method execution count, duration, and error count for server RPCs.

    The handler is reconstructed so the wrapper **executes** the real handler: a unary call is timed
    (``histogram``) and its exceptions counted (``errors``); a streaming call is iterated inside the
    wrapper so a mid-stream exception is still counted and the duration spans the whole stream (a
    guard around only the handler *call* would miss post-first-yield failures). ``CancelledError`` is
    a ``BaseException`` (not caught by ``except Exception``), so a client disconnect is not counted as
    a server error.
    """

    def __init__(
        self,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Store the metric instruments (``histogram`` required; ``executions``/``errors`` optional)."""
        self._histogram = histogram
        self._executions = executions
        self._errors = errors

    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Count the execution, then wrap the handler to record duration + errors around its run."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        method = handler_call_details.method or "unknown"
        if self._executions is not None:
            self._executions.inc(method=method)
        histogram, errors = self._histogram, self._errors

        def wrap_unary(fn: _UnaryBehavior) -> _UnaryBehavior:
            async def _timed(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> object:
                """Time the unary handler, recording its duration and counting non-abort errors."""
                start = perf_counter()
                try:
                    return await fn(request, context)
                except grpc.aio.BaseError:
                    # A deliberate abort (validation INVALID_ARGUMENT, auth UNAUTHENTICATED/PERMISSION_DENIED)
                    # is a client-caused status, not a server error — do not inflate grpc_server_errors_total.
                    raise
                except Exception:
                    if errors is not None:
                        errors.inc(method=method)
                    raise
                finally:
                    histogram.observe(perf_counter() - start, method=method)

            return _timed

        def wrap_stream(fn: _StreamBehavior) -> _StreamBehavior:
            async def _timed(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> AsyncIterator[object]:
                """Time the streaming handler, recording its duration and counting non-abort errors."""
                start = perf_counter()
                try:
                    async for item in fn(request, context):
                        yield item
                except grpc.aio.BaseError:
                    raise
                except Exception:
                    if errors is not None:
                        errors.inc(method=method)
                    raise
                finally:
                    histogram.observe(perf_counter() - start, method=method)

            return _timed

        return _rebuild_handler(handler, wrap_unary, wrap_stream)


class _LoggingServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """The transport request log — one ``info`` record per inbound RPC (the prod signal)."""

    def __init__(self, logger: Logger) -> None:
        """Store the structured logger used to record inbound RPCs."""
        self._logger = logger

    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Log the inbound RPC method at info (the one prod line), then delegate onward."""
        method = handler_call_details.method or "unknown"
        self._logger.info("grpc.server.request", method=method)
        return await continuation(handler_call_details)


class _ServiceAuthServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Rejects RPCs lacking a valid service-to-service bearer token.

    Authenticates the *calling service* so downstream claim-based authorization can trust the
    metadata it forwards. The token rides as ``authorization: Bearer <token>`` (the same header clients
    set). The expected token may be a comma-separated ``current,previous`` rotation pair; the presented
    token is compared against every accepted one with ``hmac.compare_digest`` (constant time, like Go's
    ``subtle.ConstantTimeCompare``), so response timing does not leak the secret. Dev-bypass: an empty
    expected token allows all calls.
    """

    def __init__(self, expected_token: str) -> None:
        """Precompute the accepted tokens from ``current[,previous]`` (empty disables the check)."""
        self._accepted = tuple(token.encode() for token in _split_tokens(expected_token))

    def _authorized(self, header: str) -> bool:
        """Return True iff ``header`` is ``Bearer <token>`` for an accepted token (constant-time compare)."""
        if not header.startswith(_BEARER_PREFIX):
            return False
        presented = header.removeprefix(_BEARER_PREFIX).encode()
        # Compare against every accepted token (no short-circuit) so timing does not reveal which matched.
        return any([hmac.compare_digest(presented, token) for token in self._accepted])  # noqa: C419

    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Return the real handler for authorized callers, else a handler that aborts UNAUTHENTICATED."""
        handler = await continuation(handler_call_details)
        if not self._accepted or handler is None:
            return handler
        metadata = dict(handler_call_details.invocation_metadata or [])
        if self._authorized(str(metadata.get("authorization", ""))):
            return handler
        return _deny_handler(handler)


class _ValidatingServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Runs the compiled protovalidate rules on the inbound request proto (Go ValidateInterceptor parity).

    Coded errors per ARCHITECTURE.md#error-codes (Go↔Python parity).
    The handler is reconstructed (via ``_rebuild_handler``, like recovery/metrics) so the wrapper runs
    ``protovalidate.validate`` on the request before the real handler; a rule violation becomes an
    ``INVALID_ARGUMENT`` abort carrying the violation detail (the ``CodeInvalidInput`` classification at
    the gRPC edge), and a valid request is transparent.

    Scope: only the inbound REQUEST is validated. Unlike the Go interceptor's
    ``WithValidateResponses()``, response validation (a server-output correctness check) is a deliberate
    non-goal — this interceptor's contract is request-only. A single-message request is validated; a
    client-streaming request (an async iterator) is passed through untouched (validating it would
    consume the iterator before the handler could).
    """

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler so an invalid request proto becomes an INVALID_ARGUMENT abort."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler

        def wrap_unary(fn: _UnaryBehavior) -> _UnaryBehavior:
            async def _validated(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> object:
                """Validate the request, then run the unary handler (validation aborts on failure)."""
                await _validate_request(request, context)
                return await fn(request, context)

            return _validated

        def wrap_stream(fn: _StreamBehavior) -> _StreamBehavior:
            async def _validated(
                request: object, context: grpc.aio.ServicerContext[object, object]
            ) -> AsyncIterator[object]:
                """Validate the request, then run the streaming handler (validation aborts on failure)."""
                await _validate_request(request, context)
                async for item in fn(request, context):
                    yield item

            return _validated

        return _rebuild_handler(handler, wrap_unary, wrap_stream)


async def _validate_request(
    request: object, context: grpc.aio.ServicerContext[object, object]
) -> None:
    """Validate a single-message request with protovalidate; abort INVALID_ARGUMENT on violation.

    A client-streaming request is an async iterator, not a proto ``Message`` — it is skipped
    (validating an iterator would consume it before the handler could).

    ``protovalidate`` is imported lazily here (not at module load) so importing this module — or merely
    building a ``ServerInterceptorBuilder`` in a process that serves no gRPC — does not pull the ~18MB
    compiled protovalidate/protobuf-py extension it never uses (mirrors the Bedrock lazy-import
    convention). Only ``ValidationError`` is a client fault (INVALID_ARGUMENT); a ``CompilationError`` /
    ``EvaluationError`` (a server-side rule bug, surfaced lazily on first traffic) is deliberately left to
    propagate to the outer recovery interceptor, which logs it and returns INTERNAL.
    """
    if not isinstance(request, Message):
        return
    import protovalidate  # noqa: PLC0415 — lazy: keep the heavy extension out of non-gRPC workers

    try:
        protovalidate.validate(request)
    except protovalidate.ValidationError as exc:
        await context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"invalid request: {exc}")


def _rebuild_handler[TRequest, TResponse](
    handler: grpc.RpcMethodHandler[TRequest, TResponse],
    wrap_unary: Callable[[_UnaryBehavior], _UnaryBehavior],
    wrap_stream: Callable[[_StreamBehavior], _StreamBehavior],
) -> grpc.RpcMethodHandler[TRequest, TResponse]:
    """Rebuild handler with its behaviour wrapped, preserving its (request, response) streaming shape.

    Unlike ``_deny_handler`` (which only ever installs immediately-aborting handlers), this calls the
    real handler's method through ``wrap_unary``/``wrap_stream`` so the wrapper executes — and, for a
    streaming handler, iterates — the actual behaviour. Used by the metrics and recovery interceptors.
    """
    deserializer = handler.request_deserializer
    serializer = handler.response_serializer
    # The streaming flags guarantee the matching behaviour attribute is set (gRPC contract), but the
    # stubs type each as Optional — cast to the non-None behaviour type for the branch we're in.
    if handler.request_streaming and handler.response_streaming:
        return grpc.stream_stream_rpc_method_handler(
            wrap_stream(cast("_StreamBehavior", handler.stream_stream)),
            deserializer,
            serializer,
        )
    if handler.request_streaming:
        return grpc.stream_unary_rpc_method_handler(
            wrap_unary(cast("_UnaryBehavior", handler.stream_unary)),
            deserializer,
            serializer,
        )
    if handler.response_streaming:
        return grpc.unary_stream_rpc_method_handler(
            wrap_stream(cast("_StreamBehavior", handler.unary_stream)),
            deserializer,
            serializer,
        )
    return grpc.unary_unary_rpc_method_handler(
        wrap_unary(cast("_UnaryBehavior", handler.unary_unary)), deserializer, serializer
    )


def _deny_handler[TRequest, TResponse](
    handler: grpc.RpcMethodHandler[TRequest, TResponse],
) -> grpc.RpcMethodHandler[TRequest, TResponse]:
    """Build a handler that aborts UNAUTHENTICATED, matching the original's streaming shape."""

    async def _abort_unary(
        _request: object, context: grpc.aio.ServicerContext[object, object]
    ) -> object:
        """Abort a unary call UNAUTHENTICATED (the deny-handler's unary behavior)."""
        await context.abort(grpc.StatusCode.UNAUTHENTICATED, "invalid service token")
        return None  # pragma: no cover — abort raises

    async def _abort_stream(
        _request: object, context: grpc.aio.ServicerContext[object, object]
    ) -> AsyncIterator[object]:
        """Abort a streaming call UNAUTHENTICATED (the deny-handler's streaming behavior)."""
        await context.abort(grpc.StatusCode.UNAUTHENTICATED, "invalid service token")
        yield None  # pragma: no cover — unreachable after abort

    deserializer = handler.request_deserializer
    serializer = handler.response_serializer
    if handler.request_streaming and handler.response_streaming:
        return grpc.stream_stream_rpc_method_handler(
            _abort_stream, deserializer, serializer
        )
    if handler.request_streaming:
        return grpc.stream_unary_rpc_method_handler(
            _abort_unary, deserializer, serializer
        )
    if handler.response_streaming:
        return grpc.unary_stream_rpc_method_handler(
            _abort_stream, deserializer, serializer
        )
    return grpc.unary_unary_rpc_method_handler(_abort_unary, deserializer, serializer)
