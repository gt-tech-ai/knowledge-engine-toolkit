"""Tests for the Bedrock ingestor's start: error codes and the idempotent ``clientToken``.

The aiobotocore session is mocked where the ingestor imports it; errors are real botocore
``ClientError`` shapes.
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from botocore.exceptions import ClientError

from techai_webutils.clients.kb_ingestion.bedrock.ingestor import (
    BedrockKnowledgeBaseIngestor,
)
from techai_webutils.clients.kb_ingestion.mapping import start_client_token
from techai_webutils.core.errors import AppError, ConflictError, ErrorCode

_EXPECTED_FIRST_COUNT = 64

_JOB = {"ingestionJob": {"ingestionJobId": "j1", "status": "STARTING"}}


def _ingestor(client: AsyncMock) -> BedrockKnowledgeBaseIngestor:
    """Build the ingestor whose bedrock-agent client is ``client``.

    Returns:
        The ingestor whose bedrock-agent client is ``client``.

    """
    session = MagicMock()
    session.create_client.return_value.__aenter__.return_value = client
    with patch("aiobotocore.session.get_session", return_value=session):
        return BedrockKnowledgeBaseIngestor(region="us-east-1")


def _client_error(code: str) -> ClientError:
    """Return the ``ClientError`` botocore raises for ``code``.

    On ``StartIngestionJob``.

    Returns:
        The ``ClientError`` botocore raises for ``code``.

    """
    return ClientError({"Error": {"Code": code, "Message": "m"}}, "StartIngestionJob")


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("code", "expected"),
    [
        ("ConflictException", ErrorCode.CONFLICT),
        ("ServiceQuotaExceededException", ErrorCode.UNAVAILABLE),
        ("ThrottlingException", ErrorCode.UNAVAILABLE),
    ],
)
async def test_start_maps_conflict_and_quota_codes(
    code: str, expected: ErrorCode
) -> None:
    """Test that a start refused for a running job.

    Or the account's limits raises a coded error.

    **Why this test is important:**
      - Callers reattach on ``CONFLICT``; ``UNAVAILABLE`` is transient, so the retry stack
        backs off a quota or throttle instead of failing the round.

    **What it tests:**
      - ``ConflictException`` → ``ConflictError``; quota and throttling → transient
        ``UNAVAILABLE``
    """
    client = AsyncMock()
    client.start_ingestion_job.side_effect = _client_error(code)

    with pytest.raises(AppError) as info:
        await _ingestor(client).start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )

    assert info.value.code is expected
    if expected is ErrorCode.CONFLICT:
        assert isinstance(info.value, ConflictError)
    else:
        assert info.value.is_transient


@pytest.mark.asyncio
async def test_start_validation_error_stays_permanent() -> None:
    """Test that a validation error is re-raised as the SDK raised it.

    **Why this test is important:**
      - A bad request will never succeed; retrying it would only burn the start budget.

    **What it tests:**
      - ``ValidationException`` propagates as ``ClientError``, not as a coded transient
        error
    """
    client = AsyncMock()
    client.start_ingestion_job.side_effect = _client_error("ValidationException")

    with pytest.raises(ClientError):
        await _ingestor(client).start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )


@pytest.mark.asyncio
async def test_start_retry_reuses_client_token() -> None:
    """Test that two starts of one round send the same ``clientToken``.

    And no round sends none.

    **Why this test is important:**
      - Bedrock ignores a start whose token it has seen, so a retried start never launches
        a second job; without a round the call stays as it was.

    **What it tests:**
      - two starts with ``round_id="r1"`` send one 64-character token, equal to
        ``start_client_token("kb", "ds", "r1")``; a start without a round sends no token
    """
    client = AsyncMock()
    client.start_ingestion_job.return_value = _JOB
    ingestor = _ingestor(client)

    await ingestor.start_ingestion_job(
        knowledge_base_id="kb", data_source_id="ds", round_id="r1"
    )
    await ingestor.start_ingestion_job(
        knowledge_base_id="kb", data_source_id="ds", round_id="r1"
    )
    await ingestor.start_ingestion_job(knowledge_base_id="kb", data_source_id="ds")

    first, second, third = (c.kwargs for c in client.start_ingestion_job.await_args_list)
    assert first["clientToken"] == second["clientToken"]
    assert first["clientToken"] == start_client_token("kb", "ds", "r1")
    assert len(first["clientToken"]) == _EXPECTED_FIRST_COUNT
    assert "clientToken" not in third
    assert start_client_token("kb", "ds", "r2") != first["clientToken"]
