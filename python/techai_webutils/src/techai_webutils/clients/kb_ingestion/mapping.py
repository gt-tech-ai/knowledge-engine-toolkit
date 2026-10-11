"""Pure Bedrock ``ingestionJob`` payload → ``IngestionJob`` mapping.

Split out of ``bedrock.py`` (which is carved out of unit coverage for its live aiobotocore I/O) so
the pure state-mapping logic — enum coercion + failure-reason flattening — is unit-tested and counted.
Imports no AWS SDK, so it stays on the dev/stub path's import graph without pulling aiobotocore.
"""

from __future__ import annotations

from typing import cast

from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseDocument,
)


def job_from_payload(job: dict[str, object]) -> IngestionJob:
    """Map a Bedrock ``ingestionJob`` payload dict to an ``IngestionJob``.

    ``status`` is coerced to an ``IngestionJobState`` (whose members mirror Bedrock's six statuses
    verbatim); an unknown or missing status falls back to ``FAILED`` (a safe terminal state).
    ``failureReasons`` is flattened into the ``error`` string — see ``_flatten_reasons`` for the shape
    handling.
    """
    status = str(job.get("status", IngestionJobState.FAILED.value))
    try:
        state = IngestionJobState(status)
    except ValueError:
        state = IngestionJobState.FAILED
    return IngestionJob(
        job_id=str(job["ingestionJobId"]),
        state=state,
        error=_flatten_reasons(job.get("failureReasons")),
    )


def kb_document_from_payload(detail: dict[str, object]) -> KnowledgeBaseDocument:
    """Map a Bedrock ``ListKnowledgeBaseDocuments`` ``documentDetails`` entry to a KnowledgeBaseDocument.

    Lifts the S3 source URI (``identifier.s3.uri``), the per-document ``status``, and ``statusReason``.
    A non-S3 (``custom``) or missing identifier degrades to an empty ``s3_uri`` — never raises — so the
    caller can skip it rather than crash on an unexpected identifier shape.
    """
    identifier = detail.get("identifier")
    s3 = (
        cast("dict[str, object]", identifier).get("s3")
        if isinstance(identifier, dict)
        else None
    )
    # Guard the VALUE type, not just key presence: a present-but-null uri must degrade to "" (which a
    # caller can skip), not become the literal string "None" via str(None).
    uri = cast("dict[str, object]", s3).get("uri") if isinstance(s3, dict) else None
    s3_uri = uri if isinstance(uri, str) else ""
    return KnowledgeBaseDocument(
        s3_uri=s3_uri,
        status=str(detail.get("status", "")),
        status_reason=str(detail.get("statusReason", "")),
    )


def _flatten_reasons(reasons: object) -> str:
    """Flatten Bedrock ``failureReasons`` into one ``"; "``-joined string.

    Handles the three shapes the payload can carry without dropping information: a list of reasons
    (joined), a bare scalar (coerced to its ``str``, not silently discarded), or absent/falsy (``""``).
    """
    if not reasons:
        return ""
    if isinstance(reasons, list):
        return "; ".join(str(reason) for reason in cast("list[object]", reasons))
    return str(reasons)
