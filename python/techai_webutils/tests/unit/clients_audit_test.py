"""Unit tests for the ``AuditSink`` contract, its config-selected factory and the stub sink."""

from __future__ import annotations

import dataclasses
from datetime import UTC, datetime
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.audit import (
    AuditSinkConfig,
    AuditSinkKind,
    new_audit_sink_from_config,
)
from techai_webutils.clients.audit.stub import StubAuditSink
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.audit import AuditedSource, AuditRecord, AuditSink


def _record() -> AuditRecord:
    """One audited answer with one cited source."""
    return AuditRecord(
        request_id="req-1",
        trace_id="4bf92f3577b34da6a3ce929d0e0e4736",
        tenant_id="org-1",
        workspace_id="w-1",
        user_id="auth0|u-1",
        clearance_level="internal",
        model="nova-lite",
        provider="aws.bedrock",
        prompt="what is the leave policy?",
        answer="20 days.",
        sources=(
            AuditedSource(
                document_id="doc-1",
                s3_key="org-1/doc-1.pdf",
                trust_level="high",
                classification="internal",
                score=0.91,
                page_number=3,
            ),
        ),
        prompt_tokens=120,
        completion_tokens=8,
        pii_flags=(),
        safety_flags=(),
        decision="allowed",
        cache_hit=False,
        created_at=datetime(2026, 10, 9, tzinfo=UTC),
    )


def test_audit_record_is_frozen() -> None:
    """Test that audit records and their sources are immutable.

    **Why this test is important:**
      - An audit record is evidence; it must not be editable between the call that produced it
        and the append-only store.

    **What it tests:**
      - assigning a field on ``AuditRecord`` or ``AuditedSource`` raises ``FrozenInstanceError``
      - ``sources`` and the flag collections are tuples (no in-place append)
    """
    record = _record()

    with pytest.raises(dataclasses.FrozenInstanceError):
        record.answer = "edited"  # type: ignore[misc]
    with pytest.raises(dataclasses.FrozenInstanceError):
        record.sources[0].score = 1.0  # type: ignore[misc]
    assert isinstance(record.sources, tuple)
    assert isinstance(record.pii_flags, tuple)


def test_factory_returns_stub_by_default() -> None:
    """Test that the default config builds the stub sink in shadow mode with zero infrastructure.

    **Why this test is important:**
      - Audit must never stop the graph from booting in dev or CI.

    **What it tests:**
      - the default config is ``kind="stub"``, ``enforcement="shadow"``, a 2 s timeout
      - the factory returns a ``StubAuditSink`` satisfying ``AuditSink``
      - an enforcement value other than ``shadow`` / ``enforced`` raises ``AppError(INVALID_INPUT)``
    """
    config = AuditSinkConfig()

    sink = new_audit_sink_from_config(config)

    assert (config.kind, config.enforcement, config.endpoint, config.timeout_s) == (
        AuditSinkKind.STUB,
        "shadow",
        "",
        2.0,
    )
    assert isinstance(sink, StubAuditSink)
    assert isinstance(sink, AuditSink)
    with pytest.raises(AppError) as caught:
        AuditSinkConfig(enforcement="strict")  # type: ignore[arg-type]
    assert caught.value.code is ErrorCode.INVALID_INPUT


def test_factory_uses_injected_backend_for_grpc() -> None:
    """Test that a consumer-supplied ``grpc`` kind is built by its injected factory.

    **Why this test is important:**
      - The durable audit store is behind a product service the KE must not import; the kind
        stays a config value while the backend is injected.

    **What it tests:**
      - ``kind="grpc"`` returns exactly what ``backends["grpc"]`` builds, called with the config
    """
    built = MagicMock(spec=AuditSink)
    factory = MagicMock(return_value=built)
    config = AuditSinkConfig(kind="grpc", enforcement="enforced", endpoint="aiops:8088")

    sink = new_audit_sink_from_config(config, backends={"grpc": factory})

    assert sink is built
    factory.assert_called_once_with(config)


def test_factory_unknown_kind_raises_coded_error() -> None:
    """Test that an unknown kind (including ``grpc`` with no injected backend) fails loudly.

    **Why this test is important:**
      - A misconfigured audit sink silently falling back to the stub would drop compliance
        evidence without anyone noticing.

    **What it tests:**
      - ``kind="kafka"`` and an un-injected ``kind="grpc"`` raise ``AppError(INVALID_INPUT)``
    """
    for kind in ("kafka", "grpc"):
        with pytest.raises(AppError) as caught:
            new_audit_sink_from_config(AuditSinkConfig(kind=kind))
        assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_stub_audit_sink_records_without_infra() -> None:
    """Test that the stub sink keeps each record in order, in memory.

    **Why this test is important:**
      - Tests and local runs need to observe what would have been audited without Postgres or S3.

    **What it tests:**
      - two ``record`` calls return ``None`` and ``records`` holds exactly those two, in order
    """
    sink = StubAuditSink()
    first = _record()
    second = dataclasses.replace(first, request_id="req-2")

    assert await sink.record(first) is None
    await sink.record(second)

    assert sink.records == [first, second]
