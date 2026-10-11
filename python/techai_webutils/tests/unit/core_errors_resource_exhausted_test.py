"""Unit tests for the RESOURCE_EXHAUSTED quota code and ``QuotaExceededError``.

A spent quota or budget must reach callers as HTTP 429 / gRPC RESOURCE_EXHAUSTED and must never be
retried by the resiliency stack, because a retry inside the window only burns another quota check.
"""

from typing import cast
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.decorators.proxy import RetryProxy
from techai_webutils.core.errors import AppError, ErrorCode, QuotaExceededError


def test_resource_exhausted_maps_429_and_grpc_8() -> None:
    """Test that RESOURCE_EXHAUSTED maps to HTTP 429 and gRPC 8.

    **Why this test is important:**
      - Callers decide to back off from the wire status; a quota rejection surfacing as 500 reads
        as a server bug and pages on-call instead of slowing the client down.

    **What it tests:**
      - ``ErrorCode.RESOURCE_EXHAUSTED`` has the wire value ``"RESOURCE_EXHAUSTED"``
      - ``http_status`` is 429 and ``grpc_status`` is 8
    """
    err = AppError(ErrorCode.RESOURCE_EXHAUSTED, "org budget spent")

    assert ErrorCode.RESOURCE_EXHAUSTED.value == "RESOURCE_EXHAUSTED"
    assert err.http_status == 429
    assert err.grpc_status == 8


def test_quota_exceeded_error_is_neither_transient_nor_permanent() -> None:
    """Test that ``QuotaExceededError`` carries the quota code and its details, unclassified.

    **Why this test is important:**
      - Transient would make retry loops hammer the quota; permanent would dead-letter work that
        succeeds once the window resets.

    **What it tests:**
      - code is RESOURCE_EXHAUSTED, details are exactly ``{"org_id", "reason"}``
      - ``is_transient`` and ``is_permanent`` are both False
    """
    err = QuotaExceededError(
        "monthly token budget spent", org_id="org-1", reason="budget_exhausted"
    )

    assert err.code is ErrorCode.RESOURCE_EXHAUSTED
    assert err.message == "monthly token budget spent"
    assert err.details == {"org_id": "org-1", "reason": "budget_exhausted"}
    assert err.is_transient is False
    assert err.is_permanent is False


@pytest.mark.asyncio
async def test_retry_proxy_does_not_retry_quota_exceeded() -> None:
    """Test that ``RetryProxy`` makes exactly one attempt on a quota rejection.

    **Why this test is important:**
      - The retry decorator is the one place transient classification turns into extra calls; a
        retried quota error multiplies load on the very budget check that rejected it.

    **What it tests:**
      - the ``QuotaExceededError`` propagates unchanged and the inner method is awaited once
    """
    raised = QuotaExceededError("spent", org_id="org-1", reason="budget_exhausted")
    inner = AsyncMock()
    inner.call.side_effect = raised
    proxy = cast("AsyncMock", RetryProxy(inner, max_attempts=3))

    with pytest.raises(QuotaExceededError) as caught:
        await proxy.call()

    assert caught.value is raised
    assert inner.call.await_count == 1
