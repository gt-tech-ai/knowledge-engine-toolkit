"""Tests for S3Config.client_kwargs (aiobotocore connection normalization)."""

from __future__ import annotations

from techai_webutils.clients.storage.config import S3Config


class TestS3ConfigClientKwargs:
    """S3Config.client_kwargs normalizes empty endpoint/creds to None.

    Why this test is important:
      - A service downloads objects through the S3 client. A local config defaults the
        endpoint to ``http://localhost:9000`` + ``minioadmin`` creds (dev MinIO); cloud
        environments blank them so the client uses the regional AWS endpoint + IRSA. Passing
        empty creds straight through would try to authenticate with ``""`` against real AWS.
        The empty -> None collapse is what routes a blanked config to the default (IRSA)
        credential chain. Mirrors SQSConfig.client_kwargs.

    What it tests:
      - Empty endpoint/creds map to None (regional endpoint + default credential chain).
      - Non-empty values (local MinIO) pass through unchanged; region always passes through.
    """

    def test_empty_endpoint_and_creds_collapse_to_none(self) -> None:
        """Blank endpoint and credentials become ``None`` so the regional endpoint is used.

        Why this test is important:
          - aiobotocore rejects an empty-string ``endpoint_url``; a blanked cloud config
            must not crash the service on startup.

        What it tests:
          - endpoint and both credentials map to ``None``; the region passes through.
        """
        config = S3Config(
            endpoint="", bucket="b", region="us-east-1", access_key="", secret_key=""
        )
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] is None
        assert kwargs["aws_access_key_id"] is None
        assert kwargs["aws_secret_access_key"] is None
        assert kwargs["region_name"] == "us-east-1"

    def test_non_empty_values_pass_through(self) -> None:
        """A set endpoint and credentials (local MinIO) pass through unchanged.

        Why this test is important:
          - Local dev points the client at MinIO; collapsing real values would send
            it to AWS instead.

        What it tests:
          - endpoint and both credentials are returned as configured.
        """
        config = S3Config(
            endpoint="http://localhost:9000",
            bucket="b",
            region="us-east-1",
            access_key="minioadmin",
            secret_key="minioadmin",
        )
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] == "http://localhost:9000"
        assert kwargs["aws_access_key_id"] == "minioadmin"
        assert kwargs["aws_secret_access_key"] == "minioadmin"
