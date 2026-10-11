"""Env-aware Knowledge Base client factory (the foundation/logger ``NewFromConfig`` pattern).

Selects the no-op stub (dev, no Bedrock emulator) or the real Bedrock ingestor (stage/prod)
from config (``KbConfig.kind``), so environment approximation is configuration, not
code. Unknown kinds fail loudly. The real Bedrock client is imported lazily so the dev
(stub) path never loads the AWS client.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.kb_ingestion.noop import StubKnowledgeBaseIngestor
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.kb_ingestion import KnowledgeBaseIngestor


class KbKind(StrEnum):
    """Which Knowledge Base ingestor implementation to build."""

    STUB = "stub"
    """The in-process no-op ingestor (dev/local; jobs complete instantly, no AWS)."""
    BEDROCK = "bedrock"
    """The real AWS Bedrock Agent ingestor (stage/prod)."""


@dataclass(frozen=True, slots=True)
class KbConfig:
    """Knowledge Base client configuration (the consumer maps its own config section onto it)."""

    kind: KbKind = KbKind.STUB
    """Selects the ingestor implementation (``stub`` in dev, ``bedrock`` in stage/prod)."""
    region: str = "us-east-1"
    """AWS region for the Bedrock Agent client (unused by the stub)."""
    endpoint: str | None = None
    """Optional endpoint override for the Bedrock client (e.g. a VPC endpoint / test double); ``None``
    uses AWS's default resolution. Unused by the stub."""


def new_kb_ingestor_from_config(config: KbConfig) -> KnowledgeBaseIngestor:
    """Build the KnowledgeBaseIngestor selected by ``config.kind`` (stub or bedrock)."""
    if config.kind is KbKind.STUB:
        return StubKnowledgeBaseIngestor()
    if config.kind is KbKind.BEDROCK:
        # Lazy import: keep aiobotocore off the dev/stub path (loaded only in stage/prod).
        from techai_webutils.clients.kb_ingestion.bedrock import (  # noqa: PLC0415
            BedrockKnowledgeBaseIngestor,
        )

        return BedrockKnowledgeBaseIngestor(
            region=config.region, endpoint=config.endpoint
        )
    msg = f"unknown knowledge-base kind: {config.kind!r}"
    raise AppValueError(msg)
