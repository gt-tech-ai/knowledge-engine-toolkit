"""Vector-store clients (Go Layer-2 adapters behind ``core.interfaces.vector_store``).

Vector DBs selected by config via ``new_vector_store_from_config`` (Qdrant for local dev). Package shape
: builder at top, each backend its own subpackage.
"""

from techai_webutils.clients.vector.builder import (
    VectorStoreConfig,
    VectorStoreKind,
    new_vector_store_from_config,
)

__all__ = ["VectorStoreConfig", "VectorStoreKind", "new_vector_store_from_config"]
