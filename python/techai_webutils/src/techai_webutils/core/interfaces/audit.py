"""``AuditSink`` — the contract for recording one immutable AI-audit record per answered request.

An ``AuditRecord`` captures who asked (tenant, workspace, user, clearance), what was asked and
answered, every source the answer drew on (``AuditedSource``, with its trust level and
classification), the model and token counts, the safety and PII findings, the decision, and whether
the answer came from a cache. Records are frozen values; a sink appends them and never edits one.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from datetime import datetime


@dataclass(frozen=True, slots=True)
class AuditedSource:
    """One source an audited answer drew on, with its provenance."""

    document_id: str
    """The source document's id."""
    s3_key: str
    """Where the source object is stored."""
    trust_level: str
    """The source's trust level (``high`` | ``medium`` | ``low``)."""
    classification: str
    """The source's classification (``public`` | ``internal`` | ``confidential`` | ``restricted``)."""
    score: float
    """The retrieval relevance score of the passage."""
    page_number: int | None
    """The cited page, or ``None`` when not paginated."""


@dataclass(frozen=True, slots=True, kw_only=True)
class AuditRecord:
    """One answered AI request, as recorded for audit."""

    request_id: str
    """The request's id (unique per answered request; the sink's idempotency key)."""
    trace_id: str
    """The request's trace id (hex), linking the record to its trace."""
    tenant_id: str
    """The organization/tenant — the isolation boundary."""
    workspace_id: str
    """The workspace the request ran in."""
    user_id: str
    """The authenticated subject who asked."""
    clearance_level: str
    """The requester's clearance when the answer was produced."""
    model: str
    """The model that produced the answer."""
    provider: str
    """The model provider (``aws.bedrock``, ``ollama``, ``stub``, …)."""
    prompt: str
    """The prompt as sent."""
    answer: str
    """The answer as returned."""
    sources: tuple[AuditedSource, ...]
    """Every source the answer drew on."""
    prompt_tokens: int
    """Prompt tokens consumed."""
    completion_tokens: int
    """Completion tokens produced."""
    pii_flags: tuple[str, ...]
    """PII findings on the prompt and answer."""
    safety_flags: tuple[str, ...]
    """Safety-check findings on the prompt and answer."""
    decision: str
    """What happened to the answer (``allowed`` | ``blocked`` | ``redacted``)."""
    cache_hit: bool
    """True when the answer was served from a cache (a cache hit is audited too)."""
    created_at: datetime
    """When the answer was produced (timezone-aware)."""


@runtime_checkable
class AuditSink(Protocol):
    """Appends ``AuditRecord`` values to an audit store."""

    async def record(self, record: AuditRecord) -> None:
        """Append ``record``; in shadow enforcement a backend logs a failure instead of raising."""
        ...
