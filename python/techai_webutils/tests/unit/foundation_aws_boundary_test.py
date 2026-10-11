"""Tests for the shared botocore client-boundary error translation (foundation/resilience/aws_boundary)."""

from __future__ import annotations

import pytest
from botocore.exceptions import ClientError, EndpointConnectionError

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error


def _client_error(code: str, status: int) -> ClientError:
    """Build a botocore ClientError with the given error code and HTTP status."""
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
        "Op",
    )


class TestBotocoreErrorToAppError:
    """Tests for ``botocore_error_to_app_error``."""

    @pytest.mark.parametrize(
        ("error", "expected_code"),
        [
            (_client_error("ThrottlingException", 400), ErrorCode.UNAVAILABLE),
            (_client_error("SomethingElse", 429), ErrorCode.UNAVAILABLE),
            (_client_error("ServiceUnavailableException", 503), ErrorCode.UNAVAILABLE),
            (_client_error("ValidationException", 500), ErrorCode.UNAVAILABLE),
            (_client_error("AccessDeniedException", 403), ErrorCode.INTERNAL),
            (_client_error("ResourceNotFoundException", 404), ErrorCode.INTERNAL),
            (
                EndpointConnectionError(endpoint_url="https://bedrock.example"),
                ErrorCode.UNAVAILABLE,
            ),
        ],
        ids=[
            "throttle-code",
            "http-429",
            "unavailable-503",
            "any-5xx",
            "access-denied",
            "not-found",
            "unreachable",
        ],
    )
    def test_classifies_by_error_code_and_status(
        self, error: Exception, expected_code: ErrorCode
    ) -> None:
        """A botocore failure becomes a coded AppError whose code says whether a retry can help.

        **Why this test is important:**
          - The retry decorator retries only transient AppErrors and the gRPC/HTTP edges map codes to
            statuses; a raw ClientError escapes the retry loop and is reported as INTERNAL, and a
            permission error classified as transient would be retried forever.

        **What it tests:**
          - Throttling codes, HTTP 429 and any 5xx map to UNAVAILABLE (transient); access-denied and
            not-found map to INTERNAL (terminal); a connection failure maps to UNAVAILABLE. The original
            error is kept as the cause and the operation name is in the message.
        """
        err = botocore_error_to_app_error(error, "bedrock retrieve")  # type: ignore[arg-type]

        assert isinstance(err, AppError)
        assert err.code is expected_code
        assert err.is_transient is (expected_code is ErrorCode.UNAVAILABLE)
        assert err.cause is error
        assert "bedrock retrieve" in err.message
