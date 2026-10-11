"""Tests for transient/permanent error classification across error families."""

from unittest.mock import MagicMock

import grpc
import grpc.aio
import httpx
from botocore.exceptions import ClientError, EndpointConnectionError
from qdrant_client.http.exceptions import ResponseHandlingException, UnexpectedResponse

from techai_webutils.core.errors.errors import InvalidInputError, UnavailableError
from techai_webutils.foundation.resilience.classify import is_permanent, is_transient


def _http_status_error(status: int) -> httpx.HTTPStatusError:
    """Build an httpx.HTTPStatusError carrying the given response status (as raise_for_status does)."""
    request = httpx.Request("POST", "http://ollama:11434/api/embed")
    response = httpx.Response(status, request=request)
    return httpx.HTTPStatusError("boom", request=request, response=response)


def _client_error(code: str, status: int) -> ClientError:
    """Build a botocore ClientError with the given error code + HTTP status."""
    return ClientError(
        {
            "Error": {"Code": code},
            "ResponseMetadata": {
                "RequestId": "",
                "HostId": "",
                "HTTPStatusCode": status,
                "HTTPHeaders": {},
                "RetryAttempts": 0,
            },
        },
        "SendMessage",
    )


def _rpc_error(code: grpc.StatusCode) -> MagicMock:
    """Build a mock standing in for a grpc RpcError carrying the given StatusCode.

    Specced to ``grpc.aio.AioRpcError`` — a real ``grpc.RpcError`` subclass exposing ``code()`` — so
    the classifier's ``isinstance(err, grpc.RpcError)`` + ``err.code()`` checks see a faithful
    surface (real calls raise ``grpc.Call`` subclasses, which the base ``grpc.RpcError`` lacks
    ``code()`` on).
    """
    err = MagicMock(spec=grpc.aio.AioRpcError)
    err.code.return_value = code
    return err


class TestClassify:
    """Tests for transient/permanent error classification."""

    def test_apperror_transient_vs_permanent(self) -> None:
        """Test that AppError classification delegates to its is_transient flag.

        **Why this test is important:**
          - AppError already carries transient/permanent intent; misreading it would retry
            malformed input or DLQ a recoverable timeout.

        **What it tests:**
          - UnavailableError is transient; InvalidInputError is permanent (and not transient).
        """
        assert is_transient(UnavailableError()) is True
        assert is_permanent(InvalidInputError("bad")) is True
        assert is_transient(InvalidInputError("bad")) is False

    def test_botocore_throttle_and_5xx_are_transient(self) -> None:
        """Test that AWS throttling and 5xx responses are treated as transient.

        **Why this test is important:**
          - S3/Bedrock throttling and server errors are the canonical retriable failures; treating
            them as permanent would DLQ documents on a transient AWS hiccup.

        **What it tests:**
          - A throttling code (400) and 5xx status codes classify as transient.
        """
        assert is_transient(_client_error("ThrottlingException", 400)) is True
        assert is_transient(_client_error("InternalError", 500)) is True
        assert is_transient(_client_error("ServiceUnavailable", 503)) is True

    def test_botocore_4xx_validation_is_permanent(self) -> None:
        """Test that a 4xx validation error is permanent (no retry).

        **Why this test is important:**
          - A malformed request will fail identically on every retry; retrying wastes time and
            delays DLQ routing.

        **What it tests:**
          - A ValidationException at HTTP 400 classifies as permanent.
        """
        assert is_permanent(_client_error("ValidationException", 400)) is True

    def test_endpoint_connection_error_is_transient(self) -> None:
        """Test that a botocore connection error is transient.

        **Why this test is important:**
          - Network blips reaching AWS/emulators must be retried, not treated as poison.

        **What it tests:**
          - EndpointConnectionError classifies as transient.
        """
        assert is_transient(EndpointConnectionError(endpoint_url="http://x")) is True

    def test_grpc_unavailable_and_deadline_are_transient(self) -> None:
        """Test that gRPC UNAVAILABLE / DEADLINE_EXCEEDED are transient.

        **Why this test is important:**
          - A service calling a peer over gRPC must retry a briefly-unavailable peer or a deadline,
            not fail the work item.

        **What it tests:**
          - RpcError with UNAVAILABLE and DEADLINE_EXCEEDED classify as transient; INVALID_ARGUMENT
            is permanent.
        """
        assert is_transient(_rpc_error(grpc.StatusCode.UNAVAILABLE)) is True
        assert is_transient(_rpc_error(grpc.StatusCode.DEADLINE_EXCEEDED)) is True
        assert is_permanent(_rpc_error(grpc.StatusCode.INVALID_ARGUMENT)) is True

    def test_httpx_transport_and_5xx_are_transient(self) -> None:
        """Test that httpx transport faults and 5xx/429 responses classify as transient.

        **Why this test is important:**
          - The local-vector Ollama embedder raises raw httpx errors (no error-translating
            decorator). A cold/unavailable Ollama (ConnectError, or a 503 while the model loads)
            must redrive via SQS, not be dead-lettered as poison — the exact regression that let
            the first uploads after `dev up` fail permanently.

        **What it tests:**
          - ConnectError and 500/503/429 HTTPStatusError are transient; a 400 is permanent.
        """
        assert is_transient(httpx.ConnectError("ollama down")) is True
        assert is_transient(_http_status_error(500)) is True
        assert is_transient(_http_status_error(503)) is True
        assert is_transient(_http_status_error(429)) is True
        assert is_permanent(_http_status_error(400)) is True

    def test_qdrant_transport_and_5xx_are_transient(self) -> None:
        """Test that qdrant-client transport faults and 5xx/429 responses classify as transient.

        **Why this test is important:**
          - The local-vector store raises qdrant_client exceptions (no error-translating
            decorator); a briefly-down/overloaded Qdrant must redrive, not DLQ a good document.

        **What it tests:**
          - ResponseHandlingException and a 503 UnexpectedResponse are transient; a 400
            UnexpectedResponse is permanent.
        """
        headers = httpx.Headers()
        assert is_transient(ResponseHandlingException(source=OSError("connect"))) is True
        assert (
            is_transient(UnexpectedResponse(503, "Service Unavailable", b"", headers))
            is True
        )
        assert (
            is_transient(UnexpectedResponse(429, "Too Many Requests", b"", headers))
            is True
        )
        assert is_permanent(UnexpectedResponse(400, "Bad Request", b"", headers)) is True

    def test_plain_exception_is_permanent(self) -> None:
        """Test that an unclassified exception defaults to permanent.

        **Why this test is important:**
          - Unknown errors should fail fast to the DLQ rather than retry forever; defaulting to
            permanent is the safe, non-looping choice.

        **What it tests:**
          - A plain ValueError is permanent and not transient.
        """
        assert is_permanent(ValueError("nope")) is True
        assert is_transient(ValueError("nope")) is False
