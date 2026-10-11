"""Keyword arguments shared by every aiobotocore ``create_client`` call."""

from __future__ import annotations

from typing import NotRequired, TypedDict


class AwsClientKwargs(TypedDict):
    """The endpoint, region and credential keywords a config passes to ``create_client``.

    A ``None`` value lets the SDK resolve the real regional endpoint and the default credential
    chain (IRSA in cluster) instead of a local emulator's static values.
    """

    endpoint_url: str | None
    """Service endpoint URL; ``None`` resolves the AWS regional endpoint."""
    region_name: str
    """AWS region the client signs requests for (e.g. ``us-east-1``)."""
    aws_access_key_id: str | None
    """Static access key id; ``None`` uses the default credential chain."""
    aws_secret_access_key: str | None
    """Static secret access key; ``None`` uses the default credential chain."""
    aws_session_token: NotRequired[str | None]
    """STS session token for temporary credentials; ``None`` or absent for static/IRSA ones."""
