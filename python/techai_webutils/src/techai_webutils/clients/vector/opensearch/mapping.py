"""Index names, index bodies, and entry ↔ document mapping for the OpenSearch store (pure; no I/O).

A document's fields are the entry's metadata at the top level (strings map to ``keyword`` through a
dynamic template), plus ``entry_id``, ``document_id``, ``text`` (analyzed, for BM25) and
``embedding`` (``knn_vector``). A metadata key ``entities.<label>`` holds ``|``-separated values; it
is stored as a keyword array so a term query matches one entity, and joined back on read. A
truncated (Matryoshka) index searches the vector's leading components in ``embedding`` and keeps
the full vector in ``embedding_full`` (``_source`` only) to rescore the candidates.
"""

from __future__ import annotations

import math
import re
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import (
    AppValueError,
    InternalError,
    ThrottledError,
    UnavailableError,
)
from techai_webutils.core.interfaces.retrieval import FilterEq, FilterIn, equals_all
from techai_webutils.core.interfaces.vector_store import VectorSearchResult

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.errors import AppError
    from techai_webutils.core.interfaces.retrieval import FilterExpr
    from techai_webutils.core.interfaces.vector_store import VectorEntry

ENTITY_PREFIX = "entities."
"""Metadata keys under this prefix are entity lists (``entities.person``)."""
ENTITY_SEPARATOR = "|"
"""Separates an entity list's values in ``VectorEntry.metadata``."""
EMBEDDING_FIELD = "embedding"
"""The ``knn_vector`` field the approximate search runs on."""
FULL_EMBEDDING_FIELD = "embedding_full"
"""The full-dimension vector of a truncated (Matryoshka) index, kept only in ``_source`` and read
only to rescore the truncated field's candidates."""
_FULL_VECTOR_MAPPING: dict[str, object] = {
    "type": "float",
    "index": False,
    "doc_values": False,
}
"""The full vector's mapping: neither indexed nor in doc values (which would sort the array), so
it costs disk only. A ``knn_vector`` would not do: OpenSearch 2.19 ignores ``index: false`` on one
and builds a second graph, which doubles the k-NN memory truncation exists to save."""
_FULL_COSINE = (
    "def v = params._source." + FULL_EMBEDDING_FIELD + "; double dot = 0; double dn = 0; "
    "for (int i = 0; i < params.q.length; ++i) "
    "{ double x = v[i]; dot += x * params.q[i]; dn += x * x; } "
    "return (1 + dot / Math.sqrt(dn * params.qn)) / 2;"
)
"""Painless: ``(1 + cosine) / 2`` of the stored full vector and the query, the score scale the
``cosinesimil`` k-NN search itself returns (``params.qn`` is the query's squared norm)."""
TEXT_FIELD = "text"
"""The analyzed text field BM25 searches."""
_PROMOTED = frozenset({
    "entry_id",
    "document_id",
    TEXT_FIELD,
    EMBEDDING_FIELD,
    FULL_EMBEDDING_FIELD,
})
"""Document fields that are entry fields, not metadata."""
_NAME_UNSAFE = re.compile(r"[^a-z0-9_-]+")
"""Characters an index or alias name may not hold (lowercased first)."""
_TRANSIENT_STATUSES = frozenset({429, 502, 503, 504})
"""Bulk item statuses a retry may fix."""
_TOO_MANY_REQUESTS = 429
"""The status of a bulk item the domain rejected for capacity (``es_rejected_execution_exception``)."""
_FIRST_FAILURE_STATUS = 300
"""Bulk item statuses from here up are failures."""
_NOT_FOUND = 404
"""A delete of a missing document: already gone, so not a failure."""


def safe_name(value: str) -> str:
    """Lowercase ``value`` and replace every character an index name may not hold with ``-``."""
    return _NAME_UNSAFE.sub("-", value.lower()).strip("-")


def doc_id(org_id: str, entry_id: str) -> str:
    """Return the document ``_id`` of ``entry_id`` in ``org_id``.

    Two orgs may use the same entry ids; in a shared index the org prefix keeps them two documents
    instead of one overwriting the other.
    """
    return f"{org_id}/{entry_id}"


def index_name(collection: str, model: str, version: str) -> str:
    """Return the versioned index of ``collection`` for one embedding model and version."""
    return safe_name(f"{collection}-{model}-v{version}")


def alias_name(collection: str, org_id: str | None) -> str:
    """Return the read alias: the collection, or with ``org_id`` the org's own alias of it."""
    if org_id is None:
        return safe_name(collection)
    return safe_name(f"{collection}--{org_id}")


def index_body(
    dimension: int,
    model: str,
    version: str,
    *,
    on_disk: bool,
    compression_level: str,
    truncated_dimension: int = 0,
    language_analyzers: Sequence[tuple[str, str]] = (),
) -> dict[str, object]:
    """Return the settings and mappings of a new index, stamped with its model in ``_meta``.

    ``language_analyzers`` adds one analyzed ``text.<code>`` subfield per (code, analyzer) pair.

    In memory the vector is an HNSW graph (faiss, cosine); ``on_disk`` keeps a compressed copy in
    memory and the full vectors on disk, for indices too large to hold in RAM. With a
    ``truncated_dimension`` (a Matryoshka embedder, whose leading components carry most of the
    meaning) the searched field holds only that many components, and the full vector is kept in
    ``_source`` only, to rescore the candidates; ``_meta`` records the truncation.
    """
    meta: dict[str, object] = {"model": model, "version": version}
    vectors: dict[str, object] = {
        EMBEDDING_FIELD: _vector_mapping(
            truncated_dimension or dimension,
            on_disk=on_disk,
            compression_level=compression_level,
        )
    }
    if truncated_dimension:
        meta["truncated_dimension"] = truncated_dimension
        vectors[FULL_EMBEDDING_FIELD] = dict(_FULL_VECTOR_MAPPING)
    return {
        "settings": {"index": {"knn": True}},
        "mappings": {
            "_meta": meta,
            "dynamic_templates": [_STRINGS_AS_KEYWORDS],
            "properties": vectors
            | _ENTRY_PROPERTIES
            | _text_property(language_analyzers),
        },
    }


_STRINGS_AS_KEYWORDS: dict[str, object] = {
    "strings_as_keywords": {
        "match_mapping_type": "string",
        "mapping": {"type": "keyword"},
    }
}
"""Map every metadata string as an exact-match keyword (the filters are term queries)."""
_ENTRY_PROPERTIES: dict[str, object] = {
    TEXT_FIELD: {"type": "text"},
    "entry_id": {"type": "keyword"},
    "document_id": {"type": "keyword"},
}
"""The entry fields every index maps: the BM25 text and the two ids."""


def _text_property(analyzers: Sequence[tuple[str, str]]) -> dict[str, object]:
    """Return the text field with one analyzed subfield per pair; empty adds no property."""
    if not analyzers:
        return {}
    fields = {code: {"type": "text", "analyzer": name} for code, name in analyzers}
    return {TEXT_FIELD: {"type": "text", "fields": fields}}


def text_match(query: str, analyzers: Sequence[tuple[str, str]]) -> dict[str, object]:
    """Return the BM25 clause: ``match`` on the text, or across its language subfields too.

    The query's language is unknown, so each analyzed subfield is searched and the best wins.
    """
    if not analyzers:
        return {"match": {TEXT_FIELD: query}}
    fields = [TEXT_FIELD, *(f"{TEXT_FIELD}.{code}" for code, _ in analyzers)]
    return {"multi_match": {"query": query, "fields": fields}}


def _vector_mapping(
    dimension: int, *, on_disk: bool, compression_level: str
) -> dict[str, object]:
    """Return the searched vector field: an HNSW graph in memory, or ``on_disk`` compressed."""
    vector: dict[str, object] = {
        "type": "knn_vector",
        "dimension": dimension,
        "space_type": "cosinesimil",
    }
    if on_disk:
        return vector | {"mode": "on_disk", "compression_level": compression_level}
    return vector | {"method": {"name": "hnsw", "engine": "faiss"}}


def entry_source(
    entry: VectorEntry, org_field: str, org_id: str, *, truncated_dimension: int = 0
) -> dict[str, object]:
    """Return the document stored for ``entry``: its metadata, entity lists split, and its fields.

    The org is always stored in ``org_field`` (every read filters on it), so an entry written
    through a scoped view without it in its metadata reads back with it. With a
    ``truncated_dimension`` the searched field gets the vector's leading components and the full
    vector goes to ``FULL_EMBEDDING_FIELD`` (cosine space, so the prefix needs no renormalizing).
    """
    source: dict[str, object] = {org_field: org_id}
    for key, value in entry.metadata.items():
        if key.startswith(ENTITY_PREFIX):
            source[key] = [v for v in value.split(ENTITY_SEPARATOR) if v]
        else:
            source[key] = value
    vectors: dict[str, object] = {EMBEDDING_FIELD: list(entry.embedding)}
    if truncated_dimension:
        vectors = {
            EMBEDDING_FIELD: list(entry.embedding[:truncated_dimension]),
            FULL_EMBEDDING_FIELD: list(entry.embedding),
        }
    return (
        source
        | {
            "entry_id": entry.id,
            "document_id": entry.document_id,
            TEXT_FIELD: entry.content,
        }
        | vectors
    )


def source_metadata(source: Mapping[str, object]) -> dict[str, str]:
    """Return the metadata an entry was stored with: every non-entry field, entity lists joined."""
    metadata: dict[str, str] = {}
    for key, value in source.items():
        if key in _PROMOTED:
            continue
        if isinstance(value, list):
            metadata[key] = ENTITY_SEPARATOR.join(
                str(v) for v in cast("list[object]", value)
            )
        else:
            metadata[key] = str(value)
    return metadata


def term_filters(fields: Mapping[str, str]) -> list[dict[str, object]]:
    """Return one exact-match ``term`` clause per field."""
    return filter_clauses(equals_all(fields))


def filter_clauses(expr: FilterExpr) -> list[dict[str, object]]:
    """Return ``expr`` as ``bool.filter`` clauses: ``eq`` → ``term``, ``in`` → ``terms``.

    An ``and`` contributes each of its clauses (a ``bool.filter`` list is already a conjunction).

    Raises:
        AppValueError: An ``in`` with no values (``INVALID_INPUT``).

    """
    if isinstance(expr, FilterEq):
        return [{"term": {expr.key: expr.value}}]
    if isinstance(expr, FilterIn):
        if not expr.values:
            msg = f"an in filter on {expr.key!r} needs at least one value"
            raise AppValueError(msg)
        return [{"terms": {expr.key: list(expr.values)}}]
    return [clause for inner in expr.clauses for clause in filter_clauses(inner)]


def knn_query(
    vector: Sequence[float],
    k: int,
    fields: Mapping[str, str],
    *,
    oversample_factor: float = 0.0,
    truncated_dimension: int = 0,
) -> dict[str, object]:
    """Return a k-NN search body restricted to documents matching every field exactly.

    ``oversample_factor`` > 0 sets how many candidates per result the first, approximate pass
    fetches before they are rescored at full precision; 0 leaves the domain's per-level default
    (a quantized ``on_disk`` index rescores from its full vectors on disk). Rescoring is never
    turned off: on a binary-quantized index that drops recall@10 from about 0.95 to about 0.55.
    With a ``truncated_dimension`` the first pass searches the leading components (still
    rescored by the domain on an ``on_disk`` index) and fetches ``ceil(k * oversample_factor)``
    candidates (at least ``k``), which a Painless cosine over the full vector in ``_source``
    reorders, so the full vector decides the ranking and the score (on the k-NN search's own
    ``(1 + cosine) / 2`` scale).
    """
    knn: dict[str, object] = {
        "vector": list(vector[:truncated_dimension] if truncated_dimension else vector),
        "k": k,
        "filter": {"bool": {"filter": term_filters(fields)}},
    }
    body: dict[str, object] = {
        "size": k,
        "_source": no_vectors_source(),
        "query": {"knn": {EMBEDDING_FIELD: knn}},
    }
    if not truncated_dimension:
        if oversample_factor > 0:
            knn["rescore"] = {"oversample_factor": oversample_factor}
        return body
    window = max(k, math.ceil(k * oversample_factor))
    knn["k"] = window
    body["rescore"] = _full_vector_rescore(vector, window)
    return body


def no_vectors_source() -> dict[str, object]:
    """Return the ``_source`` filter that leaves both vector fields out of every hit."""
    return {"excludes": [EMBEDDING_FIELD, FULL_EMBEDDING_FIELD]}


def _full_vector_rescore(vector: Sequence[float], window: int) -> dict[str, object]:
    """Return the rescore that reorders the top ``window`` hits by cosine on the full vector."""
    return {
        "window_size": window,
        "query": {
            "query_weight": 0.0,
            "rescore_query_weight": 1.0,
            "rescore_query": {
                "script_score": {
                    "query": {"match_all": {}},
                    "script": {
                        "source": _FULL_COSINE,
                        "params": {
                            "q": list(vector),
                            "qn": math.fsum(x * x for x in vector),
                        },
                    },
                }
            },
        },
    }


def hit_result(hit: Mapping[str, object]) -> VectorSearchResult:
    """Map a search hit to a ``VectorSearchResult``."""
    source = cast("dict[str, object]", hit.get("_source", {}))
    return VectorSearchResult(
        document_id=str(source.get("document_id", "")),
        chunk_id=str(source.get("entry_id", hit.get("_id", ""))),
        content=str(source.get(TEXT_FIELD, "")),
        score=float(cast("float", hit.get("_score", 0.0))),
        metadata=source_metadata(source),
    )


def bulk_error(response: Mapping[str, object]) -> AppError | None:
    """Return the error of a bulk response with failed items, or ``None`` when every item succeeded.

    Any item failing with 429 or 5xx makes the whole call transient (``UNAVAILABLE``, retried);
    a 429 (the domain rejecting for capacity) is a ``ThrottledError``, so the client tier counts
    it as a throttle. Otherwise it is ``INTERNAL``. Bulk writes are idempotent by id, so a retry
    rewrites the rest.
    """
    if not response.get("errors"):
        return None
    statuses: list[int] = []
    reasons: list[str] = []
    for item in cast("list[dict[str, dict[str, object]]]", response.get("items", [])):
        result = next(iter(item.values()))
        status = int(cast("int", result.get("status", 0)))
        if status >= _FIRST_FAILURE_STATUS and not (
            status == _NOT_FOUND and "delete" in item
        ):
            statuses.append(status)
            reasons.append(str(result.get("error", "")))
    if not statuses:
        return None
    msg = f"{len(statuses)} bulk items failed: {reasons[0][:300]}"
    if _TOO_MANY_REQUESTS in statuses:
        return ThrottledError(msg)
    if any(s in _TRANSIENT_STATUSES for s in statuses):
        return UnavailableError(msg)
    return InternalError(msg)


def require(value: str | None, what: str) -> str:
    """Return ``value``, or raise ``INVALID_INPUT`` naming ``what`` when it is missing."""
    if not value:
        msg = f"the opensearch store needs {what}"
        raise AppValueError(msg)
    return value
