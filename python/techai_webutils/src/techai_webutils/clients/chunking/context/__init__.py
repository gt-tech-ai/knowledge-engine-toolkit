"""Chunk contextualizers.

(``ContextualizerKind``: ``none``, ``header``, ``llm``) and their factory.
"""

from techai_webutils.clients.chunking.context.builder import (
    ContextualizerConfig,
    ContextualizerKind,
    contextualizer_from_config,
)

__all__ = ["ContextualizerConfig", "ContextualizerKind", "contextualizer_from_config"]
