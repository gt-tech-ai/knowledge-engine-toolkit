"""Pure Bedrock ``ingestionJob`` payload → ``IngestionJob`` mapping.

Split out of ``bedrock.py`` (which is carved out of unit coverage for its live aiobotocore
I/O) so the pure state-mapping logic — enum coercion + failure-reason flattening — is
unit-tested and counted. Imports no AWS SDK, so it stays on the dev/stub path's import
graph without pulling aiobotocore.
"""

from __future__ import annotations

import hashlib
from typing import cast

from techai_webutils.core.errors import AppError, ConflictError, UnavailableError
from techai_webutils.core.interfaces.kb_ingestion import (
    DocumentIngestResult,
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseDocument,
    S3Document,
)


def job_from_payload(job: dict[str, object]) -> IngestionJob:
    """Map a Bedrock ``ingestionJob`` payload dict to an ``IngestionJob``.

    ``status`` is coerced to an ``IngestionJobState`` (whose members mirror Bedrock's six
    statuses verbatim); an unknown or missing status falls back to ``FAILED`` (a safe
    terminal state). ``failureReasons`` is flattened into the ``error`` string — see
    ``_flatten_reasons`` for the shape handling. ``statistics.numberOfDocumentsScanned``
    becomes ``documents_scanned`` and ``statistics.numberOfDocumentsFailed``
    ``documents_failed`` (each 0 when absent).

    Args:
        job: The ``ingestionJob`` payload.

    Returns:
        The mapped job.

    """
    status = str(job.get("status", IngestionJobState.FAILED.value))
    try:
        state = IngestionJobState(status)
    except ValueError:
        state = IngestionJobState.FAILED
    statistics = job.get("statistics")
    return IngestionJob(
        job_id=str(job["ingestionJobId"]),
        state=state,
        error=_flatten_reasons(job.get("failureReasons")),
        documents_scanned=_statistic(statistics, "numberOfDocumentsScanned"),
        documents_failed=_statistic(statistics, "numberOfDocumentsFailed"),
    )


def kb_document_from_payload(detail: dict[str, object]) -> KnowledgeBaseDocument:
    """Map a ``documentDetails`` entry to a KnowledgeBaseDocument.

    The entry comes from Bedrock ``ListKnowledgeBaseDocuments``.

    Lifts the S3 source URI (``identifier.s3.uri``), the per-document ``status``, and
    ``statusReason``. A non-S3 (``custom``) or missing identifier degrades to an empty
    ``s3_uri`` — never raises — so the caller can skip it rather than crash on an
    unexpected identifier shape.

    Args:
        detail: One ``documentDetails`` entry.

    Returns:
        The mapped document.

    """
    identifier = detail.get("identifier")
    s3 = (
        cast("dict[str, object]", identifier).get("s3")
        if isinstance(identifier, dict)
        else None
    )
    # Guard the VALUE type, not just key presence: a present-but-null uri must degrade to
    # "" (which a caller can skip), not become the literal string "None" via str(None).
    uri = cast("dict[str, object]", s3).get("uri") if isinstance(s3, dict) else None
    s3_uri = uri if isinstance(uri, str) else ""
    return KnowledgeBaseDocument(
        s3_uri=s3_uri,
        status=str(detail.get("status", "")),
        status_reason=str(detail.get("statusReason", "")),
    )


def _flatten_reasons(reasons: object) -> str:
    """Flatten Bedrock ``failureReasons`` into one ``"; "``-joined string.

    Handles the three shapes the payload can carry without dropping information: a list of
    reasons (joined), a bare scalar (coerced to its ``str``, not silently discarded), or
    absent/falsy (``""``).

    Args:
        reasons: The payload's ``failureReasons``.

    Returns:
        The joined reasons; empty when there are none.

    """
    if not reasons:
        return ""
    if isinstance(reasons, list):
        return "; ".join(str(reason) for reason in cast("list[object]", reasons))
    return str(reasons)


def _statistic(statistics: object, key: str) -> int:
    """Return the count ``key`` from a job's ``statistics``.

    0 when it is absent or not a number.

    Args:
        statistics: The payload's ``statistics``.
        key: The statistic (``numberOfDocumentsScanned``, ``numberOfDocumentsFailed``).

    Returns:
        The count, or 0.

    """
    if not isinstance(statistics, dict):
        return 0
    value = cast("dict[str, object]", statistics).get(key)
    return value if isinstance(value, int) else 0


TRANSIENT_START_ERRORS = frozenset({
    "ServiceQuotaExceededException",
    "ThrottlingException",
})
"""``StartIngestionJob`` error codes that clear on their own: the account's job quota or
start rate."""


def start_error(code: str, data_source_id: str) -> AppError | None:
    """Map a ``StartIngestionJob`` error code to a coded error.

    ``ConflictException`` (a job already runs on the data source) is ``CONFLICT``, which
    callers rely on to reattach. A quota or throttling error is ``UNAVAILABLE``, so the
    retry stack backs off and the round is retried later. Every other code
    (``ValidationException`` included) is ``None``: the caller codes it through the
    shared botocore boundary.

    Args:
        code: The SDK error's code.
        data_source_id: The data source the start was for (named in the message).

    Returns:
        The coded error, or ``None`` for a code this mapping does not single out.

    """
    if code == "ConflictException":
        msg = f"bedrock ingestion job already running for data source {data_source_id}"
        return ConflictError(msg)
    if code in TRANSIENT_START_ERRORS:
        msg = (
            f"bedrock refused to start an ingestion job for data source "
            f"{data_source_id}: {code}"
        )
        return UnavailableError(msg)
    return None


def start_client_token(
    knowledge_base_id: str, data_source_id: str, round_id: str
) -> str | None:
    """Return the ``clientToken`` of one start round, or ``None`` when no round is named.

    The token is the SHA-256 hex of the knowledge base, data source and round (64
    characters, inside Bedrock's 33-256 alphanumeric bounds), so a retried start of the
    same round reuses it.

    Args:
        knowledge_base_id: The knowledge base.
        data_source_id: The data source within it.
        round_id: The sync round; empty sends no token.

    Returns:
        The ``clientToken`` of one start round, or ``None`` when no round is named.

    """
    if not round_id:
        return None
    seed = f"{knowledge_base_id}:{data_source_id}:{round_id}"
    return hashlib.sha256(seed.encode()).hexdigest()


COHORT_BATCH = 10
"""The most document identifiers one ``GetKnowledgeBaseDocuments`` call accepts."""


def cohort_batches(s3_uris: list[str]) -> list[list[dict[str, object]]]:
    """Split ``s3_uris`` into ``GetKnowledgeBaseDocuments`` batches.

    Each batch holds at most ten identifiers.

    Args:
        s3_uris: The documents' S3 URIs.

    Returns:
        The identifier batches.

    """
    return [
        [
            {"dataSourceType": "S3", "s3": {"uri": uri}}
            for uri in s3_uris[i : i + COHORT_BATCH]
        ]
        for i in range(0, len(s3_uris), COHORT_BATCH)
    ]


DIRECT_BATCH = 10
"""The most documents one ``IngestKnowledgeBaseDocuments`` or
``DeleteKnowledgeBaseDocuments`` call accepts (the service model's list maximum), so a
larger direct batch is sent as several calls."""


def ingest_batches(documents: list[S3Document]) -> list[list[dict[str, object]]]:
    """Split ``documents`` into ``IngestKnowledgeBaseDocuments`` ``documents`` lists.

    Each document becomes ``content`` of ``dataSourceType`` ``S3`` with its
    ``s3Location``; a document with a sidecar also gets ``metadata`` of type
    ``S3_LOCATION``. Each list holds at most ``DIRECT_BATCH`` documents.

    Args:
        documents: The documents, in order.

    Returns:
        The request lists, in order.

    """
    payloads: list[dict[str, object]] = []
    for doc in documents:
        payload: dict[str, object] = {
            "content": {"dataSourceType": "S3", "s3": {"s3Location": {"uri": doc.uri}}}
        }
        if doc.metadata_uri:
            payload["metadata"] = {
                "type": "S3_LOCATION",
                "s3Location": {"uri": doc.metadata_uri},
            }
        payloads.append(payload)
    return [payloads[i : i + DIRECT_BATCH] for i in range(0, len(payloads), DIRECT_BATCH)]


def delete_batches(uris: list[str]) -> list[list[dict[str, object]]]:
    """Split ``uris`` into ``DeleteKnowledgeBaseDocuments`` identifier lists.

    Each list holds at most ``DIRECT_BATCH`` S3 identifiers.

    Args:
        uris: The documents' S3 URIs, in order.

    Returns:
        The identifier lists, in order.

    """
    return [
        [
            {"dataSourceType": "S3", "s3": {"uri": uri}}
            for uri in uris[i : i + DIRECT_BATCH]
        ]
        for i in range(0, len(uris), DIRECT_BATCH)
    ]


def document_result_from_payload(detail: dict[str, object]) -> DocumentIngestResult:
    """Map a direct call's ``documentDetails`` entry to a ``DocumentIngestResult``.

    The URI, status and reason are read as ``kb_document_from_payload`` reads them, so a
    non-S3 identifier degrades to an empty ``uri``.

    Args:
        detail: One ``documentDetails`` entry.

    Returns:
        The mapped result.

    """
    doc = kb_document_from_payload(detail)
    return DocumentIngestResult(
        uri=doc.s3_uri, status=doc.status, status_reason=doc.status_reason
    )
