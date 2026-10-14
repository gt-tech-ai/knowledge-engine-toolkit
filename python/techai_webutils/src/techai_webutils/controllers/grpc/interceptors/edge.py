"""The gRPC server's own interceptors around the controller tier: service auth and validation.

``service_auth`` authenticates the calling service (an ``Edge.authn`` interceptor, run just before
the tier's auth layer); ``ValidatingServerInterceptor`` checks the request with protovalidate
(an ``Edge.inner`` interceptor, nearest the handler).
"""

from __future__ import annotations

import hmac
from typing import TYPE_CHECKING, override

import grpc
from google.protobuf.message import Message

from techai_webutils.controllers.grpc.interceptors.behavior import rebuild
from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.controllers.grpc.interceptors.behavior import (
        Behavior,
        Context,
        Continuation,
    )

_logger = get_logger(__name__)

_BEARER_PREFIX = "Bearer "
"""The scheme prefix of an ``authorization: Bearer <token>`` metadata value."""


def split_tokens(expected_token: str) -> tuple[str, ...]:
    """Split a ``current[,previous]`` expected token into its non-blank tokens (a rotation window)."""
    return tuple(token for part in expected_token.split(",") if (token := part.strip()))


def service_auth(
    expected_token: str, *, allow_unauthenticated: bool = False
) -> list[grpc.aio.ServerInterceptor]:
    """Return the service-to-service token check: a caller without a valid bearer token is rejected.

    ``expected_token`` may carry a comma-separated ``current,previous`` pair so a rotation has a
    window in which both are accepted; tokens are compared in constant time. A token with no
    non-blank entry raises ``AppValueError`` — a blank secret must never silently leave the service
    open — unless ``allow_unauthenticated`` (local dev), which returns no interceptor and logs a
    warning.
    """
    tokens = split_tokens(expected_token)
    if tokens:
        return [ServiceAuthServerInterceptor(tokens)]
    if not allow_unauthenticated:
        msg = (
            "service auth requires a non-empty service token "
            "(pass allow_unauthenticated=True for local dev)"
        )
        raise AppValueError(msg)
    _logger.warning(
        "gRPC service auth bypassed (allow_unauthenticated): every caller is admitted"
    )
    return []


class ServiceAuthServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Rejects RPCs lacking a valid ``authorization: Bearer <token>`` (UNAUTHENTICATED).

    Authenticates the calling service so claim-based authorization can trust the metadata it
    forwards; the presented token is compared with every accepted one in constant time.
    """

    def __init__(self, tokens: tuple[str, ...]) -> None:
        """Bind the accepted tokens."""
        self._accepted = tuple(token.encode() for token in tokens)

    def _authorized(self, header: str) -> bool:
        """Return True iff ``header`` is ``Bearer <token>`` for an accepted token."""
        if not header.startswith(_BEARER_PREFIX):
            return False
        presented = header.removeprefix(_BEARER_PREFIX).encode()
        # Compare against every accepted token (no short-circuit), so timing reveals no match.
        return any([hmac.compare_digest(presented, token) for token in self._accepted])  # noqa: C419

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Return the handler for an authorized caller, else one that aborts UNAUTHENTICATED."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        metadata = dict(handler_call_details.invocation_metadata or [])
        if self._authorized(str(metadata.get("authorization", ""))):
            return handler

        def wrap(_behavior: Behavior, _streaming: bool) -> Behavior:  # noqa: FBT001
            async def deny(_request: object, context: Context) -> object:
                await context.abort(
                    grpc.StatusCode.UNAUTHENTICATED, "invalid service token"
                )
                return None  # pragma: no cover - abort raises

            return deny

        return rebuild(handler, wrap)


class ValidatingServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Runs the compiled protovalidate rules on the request (Go ``ValidateInterceptor`` parity).

    A rule violation aborts ``INVALID_ARGUMENT`` with the violation; a valid request is
    transparent. Only a single-message request is validated: a client-streaming request (an async
    iterator) passes through, since validating it would consume it before the handler could.
    """

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler so an invalid request aborts INVALID_ARGUMENT."""
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler

        def wrap(behavior: Behavior, _streaming: bool) -> Behavior:  # noqa: FBT001
            async def validated(request: object, context: Context) -> object:
                await _validate_request(request, context)
                return await behavior(request, context)

            return validated

        return rebuild(handler, wrap)


async def _validate_request(request: object, context: Context) -> None:
    """Validate a single-message request; abort INVALID_ARGUMENT on a violation.

    ``protovalidate`` is imported lazily, so a process serving no gRPC never loads the compiled
    extension. Only ``ValidationError`` is the client's fault; a rule-compilation or evaluation
    failure (a server-side bug) propagates to the recovery layer, which returns INTERNAL.
    """
    if not isinstance(request, Message):
        return
    import protovalidate  # noqa: PLC0415 - lazy: keep the heavy extension out of non-gRPC workers

    try:
        protovalidate.validate(request)
    except protovalidate.ValidationError as exc:
        await context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"invalid request: {exc}")
