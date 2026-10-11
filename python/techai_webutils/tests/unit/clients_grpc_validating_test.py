"""Unit tests for the protovalidate request-validation server interceptor.

The interceptor is exercised through its public wrapping behaviour: it runs ``protovalidate.validate``
on the inbound request and, on a rule violation, aborts ``INVALID_ARGUMENT`` before the handler runs.
``protovalidate`` is mocked at the boundary (this suite tests the interceptor's control flow, not the
validation library, and must not import an app-specific generated proto — a layering violation).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import protovalidate
import pytest
from google.protobuf import empty_pb2

from techai_webutils.clients.rpc.grpc.interceptors.auth import (
    AuthServerInterceptor,
    HeaderClaimMapping,
)
from techai_webutils.clients.rpc.grpc.interceptors.server_builder import (
    ServerInterceptorBuilder,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _details(method: str = "/svc/M") -> grpc.HandlerCallDetails:
    """Handler-call-details carrying the RPC method name."""
    return MagicMock(method=method)


async def _wrap(
    handler_fn: AsyncMock,
) -> Callable[[object, object], Awaitable[object]]:
    """Wrap a unary handler through the validate interceptor and return its unary behavior.

    grpc's stubs type the behavior as synchronous; the aio server awaits it.
    """
    real = grpc.unary_unary_rpc_method_handler(handler_fn)
    [interceptor] = ServerInterceptorBuilder().with_validation().build()
    wrapped = await interceptor.intercept_service(
        AsyncMock(return_value=real), _details()
    )
    assert wrapped is not None
    return cast("Callable[[object, object], Awaitable[object]]", wrapped.unary_unary)


_HEADERS = HeaderClaimMapping(
    user_id="x-user-id", tenant_id="x-tenant-id", roles="x-roles"
)
"""The gateway header contract these tests configure."""


class TestValidatingServerInterceptor:
    """Behaviour of the protovalidate request-validation interceptor.

    Mirrors the Go connect ValidateInterceptor: runs the compiled proto rules on the inbound request,
    aborting INVALID_ARGUMENT on a violation and passing a valid request through unchanged.
    """

    @pytest.mark.asyncio
    async def test_validating_interceptor_rejects_invalid_request(self) -> None:
        """A request violating a compiled rule is aborted INVALID_ARGUMENT; the handler never runs.

        Why this test is important:
          - This is the whole point of the interceptor — an inbound proto that breaks a rule must be
            rejected server-side with a coded status (ARCHITECTURE.md#error-codes), not silently handled.

        What it tests:
          - On ``protovalidate.ValidationError`` the wrapped handler calls ``context.abort`` with
            ``INVALID_ARGUMENT`` and does NOT invoke the real handler.
        """
        handler_fn = AsyncMock(return_value="ok")
        wrapped = await _wrap(handler_fn)
        context = MagicMock()
        context.abort = AsyncMock(side_effect=grpc.aio.AbortError("aborted"))

        with (
            patch.object(
                protovalidate,
                "validate",
                side_effect=protovalidate.ValidationError("query is required", []),
            ),
            pytest.raises(grpc.aio.AbortError),
        ):
            await wrapped(empty_pb2.Empty(), context)

        assert context.abort.await_args.args[0] == grpc.StatusCode.INVALID_ARGUMENT
        handler_fn.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_validating_interceptor_passes_valid_request(self) -> None:
        """A valid request reaches the handler unchanged (interceptor is transparent on the happy path).

        Why this test is important:
          - Validation must not perturb valid traffic — a false positive would break every request.

        What it tests:
          - When ``protovalidate.validate`` does not raise, the real handler runs and returns its value
            and ``context.abort`` is never called.
        """
        handler_fn = AsyncMock(return_value="ok")
        wrapped = await _wrap(handler_fn)
        context = MagicMock()
        context.abort = AsyncMock()

        with patch.object(protovalidate, "validate", return_value=None):
            result = await wrapped(empty_pb2.Empty(), context)

        assert result == "ok"
        handler_fn.assert_awaited_once()
        context.abort.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_validating_interceptor_skips_non_message_request(self) -> None:
        """A non-proto request (a client-streaming async iterator) is passed through, not validated.

        Why this test is important:
          - ``_rebuild_handler`` splits by response cardinality, so a client-streaming request arrives
            as an async iterator, not a ``Message``; validating it would consume the stream. The guard
            keeps the interceptor safe for methods it cannot validate.

        What it tests:
          - When the request is not a proto ``Message``, ``protovalidate.validate`` is not called and
            the handler runs unchanged.
        """
        handler_fn = AsyncMock(return_value="ok")
        wrapped = await _wrap(handler_fn)
        context = MagicMock()
        context.abort = AsyncMock()

        with patch.object(protovalidate, "validate") as mock_validate:
            result = await wrapped("not-a-proto", context)

        assert result == "ok"
        mock_validate.assert_not_called()
        handler_fn.assert_awaited_once()

    def test_interceptor_ordering_after_auth(self) -> None:
        """``build()`` places the validate interceptor after auth (innermost, right before the handler).

        Why this test is important:
          - Validation must see the exact request the authenticated handler receives; placing it after
            auth (innermost, since the list is outermost-first) is the required contract.

        What it tests:
          - ``.with_auth(_HEADERS).with_validation().build()`` yields [auth, validate] in that order.
        """
        interceptors = (
            ServerInterceptorBuilder().with_auth(_HEADERS).with_validation().build()
        )
        names = [type(i).__name__ for i in interceptors]
        assert names == [AuthServerInterceptor.__name__, "_ValidatingServerInterceptor"]
