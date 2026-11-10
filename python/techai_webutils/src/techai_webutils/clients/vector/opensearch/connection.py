"""``opensearch_clients_from_config``: one ``AsyncOpenSearch`` per domain.

One client per configured domain.

The OpenSearch store and ``new_vector_store_from_config`` take injected clients; this
builds them from config, so a composition root never hand-wires hosts, auth and timeouts.
``sigv4`` signs every request with the ambient AWS credentials (IRSA in a cluster;
botocore refreshes them), for the Amazon OpenSearch Service domains; ``none`` is the local
dev and test container. The client never retries (``max_retries=0``): the client tier's
decorators own retry, so the two never compose.

This module imports ``opensearchpy`` and is deliberately not re-exported by the package:
only a composition root that installs the ``opensearch`` extra imports it, so the store
itself still never imports ``opensearchpy``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

import botocore.session
from opensearchpy import AsyncHttpConnection, AsyncOpenSearch, AWSV4SignerAsyncAuth

from techai_webutils.core.errors import AppValueError


class OpenSearchAuth(StrEnum):
    """How requests to the domains are authenticated."""

    NONE = "none"
    """No authentication (a local dev or test container)."""
    SIGV4 = "sigv4"
    """AWS SigV4 with the ambient credentials (Amazon OpenSearch Service; IRSA in a
    cluster)."""


@dataclass(frozen=True, slots=True)
class OpenSearchConnection:
    """Where the OpenSearch domains are and how to reach them."""

    domains: tuple[tuple[str, str], ...] = ()
    """``(name, endpoint URL)`` per domain, in shard order (domain i holds shard group
    i)."""
    auth: OpenSearchAuth = OpenSearchAuth.NONE
    """How requests are authenticated."""
    region: str = ""
    """The domains' AWS region (``sigv4`` only)."""
    service: str = "es"
    """The SigV4 service name (``es`` for managed domains, ``aoss`` for serverless)."""
    timeout_seconds: float = 10.0
    """The per-request timeout every client applies."""


def opensearch_clients_from_config(
    conn: OpenSearchConnection,
) -> dict[str, AsyncOpenSearch]:
    """Build one client per configured domain, keyed by domain name.

    The caller owns the clients and closes each (``await client.close()``) on shutdown.

    Args:
        conn: The domains, auth and timeouts.

    Returns:
        One client per configured domain, keyed by domain name.

    A callee raises ``AppValueError``: No domain, a duplicate or empty name or endpoint, a
    non-positive timeout, or ``sigv4`` without a region or with no AWS credentials
    available (``INVALID_INPUT``).

    """
    _validate(conn)
    auth = _sigv4_auth(conn) if conn.auth is OpenSearchAuth.SIGV4 else None
    return {
        name: AsyncOpenSearch(
            hosts=[endpoint],
            http_auth=auth,
            use_ssl=endpoint.startswith("https://"),
            verify_certs=endpoint.startswith("https://"),
            connection_class=AsyncHttpConnection,
            timeout=conn.timeout_seconds,
            max_retries=0,
        )
        for name, endpoint in conn.domains
    }


def _validate(conn: OpenSearchConnection) -> None:
    """Refuse a connection the clients could not use.

    Args:
        conn: The connection settings.

    Raises:
        AppValueError: The reason, as in ``opensearch_clients_from_config``.

    """
    if not conn.domains:
        msg = "opensearch: at least one domain is required"
        raise AppValueError(msg)
    names = [name for name, _ in conn.domains]
    if len(set(names)) != len(names) or not all(names):
        msg = f"opensearch: domain names must be unique and non-empty, got {names}"
        raise AppValueError(msg)
    if not all(endpoint for _, endpoint in conn.domains):
        msg = "opensearch: every domain needs an endpoint"
        raise AppValueError(msg)
    if conn.timeout_seconds <= 0:
        msg = f"opensearch: timeout_seconds must be positive, got {conn.timeout_seconds}"
        raise AppValueError(msg)
    if conn.auth is OpenSearchAuth.SIGV4 and not conn.region:
        msg = "opensearch: sigv4 auth needs the domains' region"
        raise AppValueError(msg)


def _sigv4_auth(conn: OpenSearchConnection) -> AWSV4SignerAsyncAuth:
    """Return the SigV4 signer over the ambient (refreshing) AWS credentials.

    Args:
        conn: The connection settings.

    Returns:
        The SigV4 signer over the ambient (refreshing) AWS credentials.

    Raises:
        AppValueError: No AWS credentials are available (``INVALID_INPUT``).

    """
    credentials = botocore.session.get_session().get_credentials()
    if credentials is None:
        msg = "opensearch: sigv4 auth found no AWS credentials"
        raise AppValueError(msg)
    return AWSV4SignerAsyncAuth(credentials, conn.region, conn.service)
