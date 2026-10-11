"""S3 client configuration."""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.types.aws_client import AwsClientKwargs


@dataclass(frozen=True)
class S3Config:
    """Configuration for S3-compatible storage client."""

    endpoint: str
    """S3-compatible endpoint URL; empty resolves the real AWS regional S3 endpoint."""
    bucket: str
    """Name of the object-storage bucket to read from and write to."""
    region: str
    """AWS region for the S3 client (e.g. ``us-east-1``)."""
    access_key: str = ""
    """Static access key id; empty falls back to the default AWS credential chain (IRSA)."""
    secret_key: str = ""
    """Static secret access key; empty falls back to the default AWS credential chain."""
    session_token: str = ""
    """STS session token for temporary credentials; empty for static/IRSA credentials."""

    def client_kwargs(self) -> AwsClientKwargs:
        """Keyword args for ``aiobotocore``'s ``create_client("s3", ...)``.

        Empty ``endpoint``/``access_key``/``secret_key`` collapse to ``None`` so the SDK
        resolves the real AWS regional S3 endpoint and the default credential chain (IRSA in
        cluster) instead of the local MinIO dev defaults (``http://localhost:9000`` + the
        ``minioadmin`` static creds). Mirrors ``SQSConfig.client_kwargs`` and the Go S3
        client's kind gate. ``session_token`` is set for STS temp credentials (the connector
        iam_role read path assumes the connector's role and signs with the returned token);
        empty collapses to ``None`` so the static/IRSA paths are unaffected.
        """
        return {
            "endpoint_url": self.endpoint or None,
            "region_name": self.region,
            "aws_access_key_id": self.access_key or None,
            "aws_secret_access_key": self.secret_key or None,
            "aws_session_token": self.session_token or None,
        }
