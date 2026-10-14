"""GenAI span enrichment for LLM and retrieval clients (the client tier is ``clients.client``)."""

from techai_webutils.clients.decorators.ai_enricher import (
    GEN_AI_SYSTEM_BY_PROVIDER,
    AiSpanEnricher,
)

__all__ = ["GEN_AI_SYSTEM_BY_PROVIDER", "AiSpanEnricher"]
