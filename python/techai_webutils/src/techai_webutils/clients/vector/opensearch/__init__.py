"""The ``opensearch`` vector store: org-isolated, versioned indices.

And a multi-domain router.
"""

from techai_webutils.clients.vector.opensearch.config import (
    IndexLayout,
    OpenSearchConfig,
    VectorMode,
)
from techai_webutils.clients.vector.opensearch.mapping import index_name
from techai_webutils.clients.vector.opensearch.multi_domain import MultiDomainVectorStore
from techai_webutils.clients.vector.opensearch.store import OpenSearchVectorStore

__all__ = [
    "IndexLayout",
    "MultiDomainVectorStore",
    "OpenSearchConfig",
    "OpenSearchVectorStore",
    "VectorMode",
    "index_name",
]
