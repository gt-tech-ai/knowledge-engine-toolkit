"""Chunk contextualizers and their factory.

The kinds are ``none``, ``header`` and ``llm`` (``ContextualizerKind``).
"""

from techai_webutils.clients.chunking.context.builder import (
    ContextualizerConfig,
    ContextualizerKind,
    contextualizer_from_config,
)

__all__ = ["ContextualizerConfig", "ContextualizerKind", "contextualizer_from_config"]
