"""Bedrock Knowledge Base ingestor backend (aiobotocore bedrock-agent) — stage/prod."""

from techai_webutils.clients.kb_ingestion.bedrock.ingestor import (
    BedrockKnowledgeBaseIngestor,
)

__all__ = ["BedrockKnowledgeBaseIngestor"]
