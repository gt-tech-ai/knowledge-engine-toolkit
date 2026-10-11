"""Knowledge-base clients (adapters behind ``core.interfaces.knowledge_base``).

The chunk-indexing ``KnowledgeBase`` contract (index/remove/status/sync), selected by config via
``new_knowledge_base_from_config``. Distinct from ``clients/kb_ingestion`` (the Bedrock
``KnowledgeBaseIngestor`` start/poll job). Package shape: builder at top, each backend its own
subpackage.
"""

from techai_webutils.clients.vector_kb.builder import (
    KnowledgeBaseConfig,
    KnowledgeBaseKind,
    new_knowledge_base_from_config,
)

__all__ = ["KnowledgeBaseConfig", "KnowledgeBaseKind", "new_knowledge_base_from_config"]
