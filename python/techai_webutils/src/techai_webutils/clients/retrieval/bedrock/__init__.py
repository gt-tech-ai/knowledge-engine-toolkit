"""Bedrock Knowledge Base retrieval-engine backend (aiobotocore bedrock-agent-runtime).

Stage/prod.
"""

from techai_webutils.clients.retrieval.bedrock.engine import (
    SOURCE_URI_KEY,
    BedrockRetrievalEngine,
    DocumentIdResolver,
    FilterBuilder,
    metadata_document_id,
)

__all__ = [
    "SOURCE_URI_KEY",
    "BedrockRetrievalEngine",
    "DocumentIdResolver",
    "FilterBuilder",
    "metadata_document_id",
]
