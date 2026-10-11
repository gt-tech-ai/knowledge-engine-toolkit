"""Tests for SQSConfig.client_kwargs (aiobotocore connection normalization)."""

from __future__ import annotations

from techai_webutils.clients.messaging.config import SQSConfig


class TestSQSConfigClientKwargs:
    """SQSConfig.client_kwargs normalizes empty endpoint/creds to None.

    Why this test is important:
      - Every SQS client (publisher, subscriber, DLQ backend, per-app adapters) builds its
        aiobotocore ``create_client`` kwargs from here. base.yaml defaults the endpoint to
        ``http://localhost:9324`` and creds to ``local`` (dev ElasticMQ); cloud environments
        blank them so the client uses the regional AWS endpoint + IRSA. aiobotocore rejects an
        empty-string ``endpoint_url`` outright (``ValueError: Invalid endpoint:``), which
        crashes a service on startup — so the empty -> None collapse is the
        single guard that keeps a blanked config from taking down the service.

    What it tests:
      - Empty endpoint/creds map to ``None`` (regional endpoint + default credential chain).
      - Non-empty values (local ElasticMQ) pass through unchanged; region always passes through.
    """

    def test_empty_endpoint_and_creds_collapse_to_none(self) -> None:
        """Blank endpoint and credentials become ``None`` so the regional endpoint is used.

        Why this test is important:
          - aiobotocore rejects an empty-string ``endpoint_url``; a blanked cloud config
            must not crash the service on startup.

        What it tests:
          - endpoint and both credentials map to ``None``; the region passes through.
        """
        config = SQSConfig(
            endpoint="", region="us-east-1", queue_url="q", access_key="", secret_key=""
        )
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] is None
        assert kwargs["aws_access_key_id"] is None
        assert kwargs["aws_secret_access_key"] is None
        assert kwargs["region_name"] == "us-east-1"

    def test_non_empty_values_pass_through(self) -> None:
        """A set endpoint and credentials (local ElasticMQ) pass through unchanged.

        Why this test is important:
          - Local dev points the client at ElasticMQ; collapsing real values would send
            it to AWS instead.

        What it tests:
          - endpoint and both credentials are returned as configured.
        """
        config = SQSConfig(
            endpoint="http://localhost:9324",
            region="us-east-1",
            queue_url="q",
            access_key="local",
            secret_key="local",
        )
        kwargs = config.client_kwargs()
        assert kwargs["endpoint_url"] == "http://localhost:9324"
        assert kwargs["aws_access_key_id"] == "local"
        assert kwargs["aws_secret_access_key"] == "local"
