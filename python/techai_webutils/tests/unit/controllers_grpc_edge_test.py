"""Tests for the gRPC server's edge interceptors: service auth and request validation.

``protovalidate`` is mocked at the boundary: these tests cover the interceptors' control flow,
not the validation library, and must not import an app-specific generated proto.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import protovalidate
import pytest
from google.protobuf import empty_pb2

from techai_webutils.controllers.grpc.interceptors.edge import (
    ServiceAuthServerInterceptor,
    ValidatingServerInterceptor,
    service_auth,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _details(metadata: list[tuple[str, str]] | None = None) -> grpc.HandlerCallDetails:
    """Handler-call-details carrying the RPC method and ``metadata``."""
    return MagicMock(method="/svc/M", invocation_metadata=metadata or [])


def _unary(
    handler: grpc.RpcMethodHandler[object, object] | None,
) -> Callable[[object, object], Awaitable[object]]:
    """Return a handler's unary behavior; grpc's stubs type it as synchronous."""
    assert handler is not None
    return cast("Callable[[object, object], Awaitable[object]]", handler.unary_unary)


class TestServiceAuth:
    """The service-to-service bearer-token check."""

    @staticmethod
    def _only(token: str) -> grpc.aio.ServerInterceptor:
        """Return the single interceptor ``service_auth(token)`` builds."""
        [interceptor] = service_auth(token)
        return interceptor

    @pytest.mark.asyncio
    async def test_allows_a_valid_token_and_rejects_a_wrong_one(self) -> None:
        """A matching bearer token reaches the handler; a wrong one aborts UNAUTHENTICATED.

        Why this test is important:
          - This is the security boundary: a rogue in-cluster caller forging identity metadata
            must be rejected before the handler runs.

        What it tests:
          - ``Bearer secret`` returns the real handler; ``Bearer wrong`` returns a substitute
            whose call aborts UNAUTHENTICATED.
        """
        interceptor = self._only("secret")
        handler = grpc.unary_unary_rpc_method_handler(AsyncMock(return_value="ok"))
        valid = _details([("authorization", "Bearer secret")])
        wrong = _details([("authorization", "Bearer wrong")])

        assert await interceptor.intercept_service(
            AsyncMock(return_value=handler), valid
        ) is (handler)
        denied = await interceptor.intercept_service(
            AsyncMock(return_value=handler), wrong
        )
        assert denied is not handler
        context = MagicMock()
        context.abort = AsyncMock()
        await _unary(denied)("req", context)
        assert context.abort.await_args.args[0] == grpc.StatusCode.UNAUTHENTICATED

    @pytest.mark.asyncio
    async def test_accepts_both_tokens_of_a_rotation_pair(self) -> None:
        """A ``current,previous`` token admits both during a rotation window, nothing else.

        Why this test is important:
          - Rotating the shared token without downtime needs both to pass for a while, as the Go
            validator allows; a blank entry must never become an accepted empty token.

        What it tests:
          - Both tokens pass; a wrong token, the whole comma string and no header are denied.
        """
        interceptor = self._only("new-token, old-token")
        handler = grpc.unary_unary_rpc_method_handler(AsyncMock())

        async def admitted(metadata: list[tuple[str, str]]) -> bool:
            result = await interceptor.intercept_service(
                AsyncMock(return_value=handler), _details(metadata)
            )
            return result is handler

        assert await admitted([("authorization", "Bearer new-token")])
        assert await admitted([("authorization", "Bearer old-token")])
        assert not await admitted([("authorization", "Bearer wrong")])
        assert not await admitted([("authorization", "Bearer new-token, old-token")])
        assert not await admitted([])

    def test_a_blank_token_fails_loud_unless_explicitly_bypassed(self) -> None:
        """A blank token never silently leaves the server open.

        Why this test is important:
          - A missing secret in a deployed environment used to drop the check; the bypass must
            be an explicit local-dev choice.

        What it tests:
          - ``""`` and ``" , "`` raise ``AppValueError``; with ``allow_unauthenticated`` no
            interceptor is built.
        """
        assert isinstance(self._only("secret"), ServiceAuthServerInterceptor)
        for blank in ("", " , "):
            with pytest.raises(AppValueError, match="service token"):
                service_auth(blank)
        assert service_auth("", allow_unauthenticated=True) == []

    @pytest.mark.asyncio
    async def test_an_unknown_method_is_left_for_grpc(self) -> None:
        """A method with no handler stays unhandled, so gRPC answers UNIMPLEMENTED.

        Why this test is important:
          - A deny handler for a missing method would hide the routing error behind
            UNAUTHENTICATED.

        What it tests:
          - intercept_service returns None when the continuation does.
        """
        result = await self._only("secret").intercept_service(
            AsyncMock(return_value=None), _details()
        )
        assert result is None


class TestValidatingServerInterceptor:
    """The protovalidate request check (Go ``ValidateInterceptor`` parity)."""

    @staticmethod
    async def _wrap(
        handler_fn: AsyncMock,
    ) -> Callable[[object, object], Awaitable[object]]:
        """Wrap a unary handler through the interceptor and return its behavior."""
        real = grpc.unary_unary_rpc_method_handler(handler_fn)
        wrapped = await ValidatingServerInterceptor().intercept_service(
            AsyncMock(return_value=real), _details()
        )
        return _unary(wrapped)

    @pytest.mark.asyncio
    async def test_rejects_an_invalid_request(self) -> None:
        """A request violating a rule aborts INVALID_ARGUMENT before the handler runs.

        Why this test is important:
          - An inbound proto that breaks a rule must be rejected with a coded status.

        What it tests:
          - On ``ValidationError`` the call aborts INVALID_ARGUMENT and the handler is not run.
        """
        handler_fn = AsyncMock(return_value="ok")
        wrapped = await self._wrap(handler_fn)
        context = MagicMock()
        context.abort = AsyncMock(side_effect=grpc.aio.AbortError("aborted"))
        violation = protovalidate.ValidationError("query is required", [])

        with (
            patch.object(protovalidate, "validate", side_effect=violation),
            pytest.raises(grpc.aio.AbortError),
        ):
            await wrapped(empty_pb2.Empty(), context)

        assert context.abort.await_args.args[0] == grpc.StatusCode.INVALID_ARGUMENT
        handler_fn.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_passes_a_valid_or_non_message_request(self) -> None:
        """A valid request, or a non-proto one (a client stream), reaches the handler unchanged.

        Why this test is important:
          - Validation must not perturb valid traffic, and validating a client stream would
            consume it before the handler could.

        What it tests:
          - A valid message runs the handler; a non-message request is not validated at all.
        """
        handler_fn = AsyncMock(return_value="ok")
        wrapped = await self._wrap(handler_fn)
        context = MagicMock()

        with patch.object(protovalidate, "validate", return_value=None) as validate:
            assert await wrapped(empty_pb2.Empty(), context) == "ok"
            assert await wrapped("not-a-proto", context) == "ok"

        validate.assert_called_once()
        assert handler_fn.await_count == 2

    @pytest.mark.asyncio
    async def test_an_unknown_method_is_left_for_grpc(self) -> None:
        """A method with no handler stays unhandled.

        Why this test is important:
          - Wrapping a missing handler would crash instead of answering UNIMPLEMENTED.

        What it tests:
          - intercept_service returns None when the continuation does.
        """
        result = await ValidatingServerInterceptor().intercept_service(
            AsyncMock(return_value=None), _details()
        )
        assert result is None
