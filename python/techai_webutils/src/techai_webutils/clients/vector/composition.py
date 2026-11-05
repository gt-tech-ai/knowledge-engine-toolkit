"""Former home of the embedder/store dimension check, now ``foundation.vectors``; kept so old imports resolve.

The embedder and store pair is no longer built here: a family may not import a sibling family, so
the caller builds the embedder (``clients.embedding``) and the store (``clients.vector``) and hands
them to the retrieval or knowledge-base factory.
"""

from techai_webutils.foundation.vectors import require_matching_dimension

__all__ = ["require_matching_dimension"]
