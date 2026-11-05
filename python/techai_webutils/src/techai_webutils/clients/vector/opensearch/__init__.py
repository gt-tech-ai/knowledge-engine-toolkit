"""The ``opensearch`` vector store: org-isolated, versioned indices, and a multi-domain router."""

from techai_webutils.clients.vector.opensearch.config import IndexLayout, OpenSearchConfig
from techai_webutils.clients.vector.opensearch.mapping import index_name
from techai_webutils.clients.vector.opensearch.multi_domain import MultiDomainVectorStore
from techai_webutils.clients.vector.opensearch.store import OpenSearchVectorStore

__all__ = [
    "IndexLayout",
    "MultiDomainVectorStore",
    "OpenSearchConfig",
    "OpenSearchVectorStore",
    "index_name",
]
