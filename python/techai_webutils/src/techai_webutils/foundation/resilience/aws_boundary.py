"""Shared botocore client-boundary error translation (ARCHITECTURE.md#error-codes).

Maps a raw botocore ``ClientError`` / ``BotoCoreError`` raised by an AWS SDK call into a coded
``AppError`` at the client boundary, so a throttle or a transient 5xx surfaces as a transient
``AppError`` the composition-root resiliency stack (RetryProxy) can retry and the transport edges map
to UNAVAILABLE — instead of a raw SDK error escaping the retry loop. It lives in foundation (which
every client imports downward) so the AWS clients share ONE boundary rather than a lateral
clients→clients import; ``grpc_boundary`` is its gRPC sibling.
"""

from __future__ import annotations

from botocore.exceptions import BotoCoreError, ClientError

from techai_webutils.core.errors import AppError, ErrorCode

# ClientError codes worth a retry — throttling, transient 5xx, and model-side timeouts. Everything
# else (access-denied, ResourceNotFound for a legacy/unavailable model or index, validation) is a
# terminal misconfiguration/permission error.
_TRANSIENT_CODES = frozenset({
    "ThrottlingException",
    "ServiceUnavailableException",
    "ServiceQuotaExceededException",
    "ModelTimeoutException",
    "ModelNotReadyException",
    "InternalServerException",
})
"""ClientError codes worth a retry (throttling, transient 5xx, model-side timeouts)."""
_SERVER_ERROR_STATUS = 500
"""Lowest HTTP status classifying a response as a server error (retryable)."""
_TOO_MANY_REQUESTS_STATUS = 429
"""HTTP status classifying a response as throttled (retryable)."""


def botocore_error_to_app_error(
    exc: ClientError | BotoCoreError, operation: str
) -> AppError:
    """Map a botocore failure of ``operation`` (e.g. ``"bedrock retrieve"``) to a coded ``AppError``.

    A ``ClientError`` is classified by its error code / HTTP status — throttling / 429 / 5xx /
    model-timeout → ``UNAVAILABLE`` (transient, retryable); everything else (access-denied,
    ``ResourceNotFound``, validation) → ``INTERNAL`` (terminal). Any other ``BotoCoreError``
    (connect/read timeout, endpoint failure) → ``UNAVAILABLE``. The SDK error is kept as the cause.
    Raw botocore exceptions must not cross a client boundary: the resilience decorators and the
    transport status mapping derive their behaviour from the ``ErrorCode``.
    """
    if isinstance(exc, ClientError):
        err = exc.response.get("Error", {})
        code = str(err.get("Code", ""))
        status = int(
            exc.response.get("ResponseMetadata", {}).get("HTTPStatusCode", 0) or 0
        )
        transient = (
            code in _TRANSIENT_CODES
            or status >= _SERVER_ERROR_STATUS
            or status == _TOO_MANY_REQUESTS_STATUS
        )
        app_code = ErrorCode.UNAVAILABLE if transient else ErrorCode.INTERNAL
        return AppError(
            app_code, f"{operation} failed: {code or status or 'error'}", cause=exc
        )
    return AppError(ErrorCode.UNAVAILABLE, f"{operation} unreachable: {exc}", cause=exc)
