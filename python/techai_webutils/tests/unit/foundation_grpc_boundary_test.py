"""Tests for the shared gRPC client-boundary error translation (foundation/resilience/grpc_boundary)."""

from __future__ import annotations

import asyncio

import grpc
import pytest
from grpc.aio import AioRpcError, Metadata

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.foundation.resilience.grpc_boundary import (
    grpc_error_to_app_error,
    wrap_grpc_errors,
)


def _rpc_error(
    code: grpc.StatusCode, detail: str = "boom", trailing: Metadata | None = None
) -> AioRpcError:
    """Build a raw AioRpcError for a status code (matching the SDK's positional constructor)."""
    return AioRpcError(
        code, Metadata(), trailing if trailing is not None else Metadata(), detail
    )


class TestGrpcErrorToAppError:
    """Tests for ``grpc_error_to_app_error``."""

    def test_transient_codes_map_to_transient_app_error(self) -> None:
        """Test that UNAVAILABLE / DEADLINE_EXCEEDED become transient AppErrors.

        **Why this test is important:**
          - RetryProxy only retries a transient ``AppError``; a raw ``AioRpcError`` escapes the retry
            loop on the first attempt. The boundary must classify these as transient so every gRPC
            client wrapped with it gets a retryable error.

        **What it tests:**
          - Each transient gRPC status yields an ``AppError`` with ``is_transient`` True (never the raw error).
        """
        for code in (
            grpc.StatusCode.UNAVAILABLE,
            grpc.StatusCode.DEADLINE_EXCEEDED,
        ):
            err = grpc_error_to_app_error(_rpc_error(code))
            assert isinstance(err, AppError)
            assert err.is_transient
            assert not isinstance(err, AioRpcError)

    def test_terminal_code_maps_to_non_transient_app_error(self) -> None:
        """Test that a terminal status (NOT_FOUND) becomes a non-transient AppError.

        **Why this test is important:**
          - A terminal gRPC status must NOT be retried; surfacing it as a non-transient ``AppError``
            lets the caller fail fast instead of the resiliency stack looping on a permanent error.

        **What it tests:**
          - NOT_FOUND yields an ``AppError`` with ``is_transient`` False.
        """
        err = grpc_error_to_app_error(_rpc_error(grpc.StatusCode.NOT_FOUND))
        assert isinstance(err, AppError)
        assert not err.is_transient

    def test_grpc_boundary_maps_resource_exhausted_to_quota_code(self) -> None:
        """Test that a bare RESOURCE_EXHAUSTED becomes the non-retryable quota code.

        **Why this test is important:**
          - A server rejecting on a spent quota answers RESOURCE_EXHAUSTED; treating it as transient
            makes the retry stack re-send the call into the same exhausted budget.

        **What it tests:**
          - the AppError code is RESOURCE_EXHAUSTED, ``is_transient`` and ``is_permanent`` are False
          - the gRPC detail is kept in the message and the raw error is the cause
        """
        raw = _rpc_error(grpc.StatusCode.RESOURCE_EXHAUSTED, "budget spent")

        err = grpc_error_to_app_error(raw)

        assert err.code is ErrorCode.RESOURCE_EXHAUSTED
        assert err.is_transient is False
        assert err.is_permanent is False
        assert err.message == "gRPC upstream RESOURCE_EXHAUSTED: budget spent"
        assert err.cause is raw

    def test_grpc_boundary_resource_exhausted_with_pushback_is_transient(self) -> None:
        """Test that RESOURCE_EXHAUSTED with an explicit retry pushback stays retryable.

        **Why this test is important:**
          - A rate limiter that names a retry delay is telling the caller the call will succeed
            after it; dropping that signal would fail calls the server invited back.

        **What it tests:**
          - ``grpc-retry-pushback-ms: 250`` yields a transient UNAVAILABLE with ``retry_after_ms`` "250"
          - ``retry-after: 2`` (seconds) yields a transient UNAVAILABLE with ``retry_after_ms`` "2000"
        """
        pushback = grpc_error_to_app_error(
            _rpc_error(
                grpc.StatusCode.RESOURCE_EXHAUSTED,
                trailing=Metadata(("grpc-retry-pushback-ms", "250")),
            )
        )
        retry_after = grpc_error_to_app_error(
            _rpc_error(
                grpc.StatusCode.RESOURCE_EXHAUSTED,
                trailing=Metadata(("retry-after", "2")),
            )
        )

        assert pushback.code is ErrorCode.UNAVAILABLE
        assert pushback.is_transient is True
        assert pushback.details == {"retry_after_ms": "250"}
        assert retry_after.code is ErrorCode.UNAVAILABLE
        assert retry_after.details == {"retry_after_ms": "2000"}

    def test_grpc_boundary_ignores_malformed_pushback(self) -> None:
        """Test that an unparseable pushback trailer is treated as a plain quota rejection.

        **Why this test is important:**
          - A garbage trailer must not turn a quota rejection into a retry storm.

        **What it tests:**
          - ``grpc-retry-pushback-ms: soon`` and a negative ``retry-after`` both yield RESOURCE_EXHAUSTED
        """
        for trailing in (
            Metadata(("grpc-retry-pushback-ms", "soon")),
            Metadata(("retry-after", "-1")),
        ):
            err = grpc_error_to_app_error(
                _rpc_error(grpc.StatusCode.RESOURCE_EXHAUSTED, trailing=trailing)
            )
            assert err.code is ErrorCode.RESOURCE_EXHAUSTED


class TestWrapGrpcErrors:
    """Tests for ``wrap_grpc_errors``."""

    @pytest.mark.asyncio
    async def test_wraps_raw_rpc_error_into_app_error(self) -> None:
        """Test that the decorator translates a raised AioRpcError into a coded AppError.

        **Why this test is important:**
          - Client methods raise raw ``AioRpcError`` from the stub; the decorator is what turns that
            into the coded ``AppError`` the resiliency stack understands — the shared mechanism every
            wrapped gRPC client relies on.

        **What it tests:**
          - A decorated coroutine raising ``AioRpcError(UNAVAILABLE)`` instead raises a transient
            ``AppError`` (not the raw SDK error).
        """

        @wrap_grpc_errors
        async def call() -> None:
            await asyncio.sleep(0)
            raise _rpc_error(grpc.StatusCode.UNAVAILABLE)

        with pytest.raises(AppError) as excinfo:
            await call()
        assert excinfo.value.is_transient
        assert not isinstance(excinfo.value, AioRpcError)
