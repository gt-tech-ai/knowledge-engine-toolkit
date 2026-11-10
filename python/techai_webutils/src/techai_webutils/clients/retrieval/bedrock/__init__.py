"""Bedrock Knowledge Base retrieval-engine backend (stage/prod).

It calls aiobotocore's bedrock-agent-runtime.
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
