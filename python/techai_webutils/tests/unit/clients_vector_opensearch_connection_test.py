"""Unit tests for ``opensearch_clients_from_config``: one client per domain, with its auth."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from techai_webutils.clients.vector.opensearch.connection import (
    OpenSearchAuth,
    OpenSearchConnection,
    opensearch_clients_from_config,
)
from techai_webutils.core.errors import AppValueError

_MODULE = "techai_webutils.clients.vector.opensearch.connection"


def test_builds_one_client_per_domain_that_never_retries() -> None:
    """Test that every configured domain gets its own client, keyed by name.

    **Why this test is important:**
      - A cell's shards map onto domains by name; a client pointed at the wrong endpoint,
        or one that retried on its own, would write a shard to another domain or double
        every retry the client tier already makes.

    **What it tests:**
      - Two domains build two clients keyed by name, each with its own endpoint, the
        configured timeout, TLS only for an ``https`` endpoint, no auth for ``none``, and
        ``max_retries=0``.
    """
    conn = OpenSearchConnection(
        domains=(("d0", "https://d0.example"), ("d1", "http://localhost:9200")),
        timeout_seconds=3.0,
    )
    with patch(f"{_MODULE}.AsyncOpenSearch") as client_cls:
        clients = opensearch_clients_from_config(conn)

    assert set(clients) == {"d0", "d1"}
    calls = {c.kwargs["hosts"][0]: c.kwargs for c in client_cls.call_args_list}
    assert calls["https://d0.example"]["use_ssl"] is True
    assert calls["http://localhost:9200"]["use_ssl"] is False
    for kwargs in calls.values():
        assert kwargs["timeout"] == pytest.approx(3.0)
        assert kwargs["max_retries"] == 0
        assert kwargs["http_auth"] is None


def test_sigv4_signs_with_the_ambient_credentials() -> None:
    """Test that ``sigv4`` builds the signer over the ambient credentials and region.

    **Why this test is important:**
      - Amazon OpenSearch Service domains accept only SigV4-signed requests from the
        IRSA roles their access policy names; an unsigned client is refused on every call.

    **What it tests:**
      - The signer is built from the botocore session's credentials, the region and the
        ``es`` service, and handed to the client as its ``http_auth``.
    """
    credentials = MagicMock(name="credentials")
    session = MagicMock()
    session.get_credentials.return_value = credentials
    conn = OpenSearchConnection(
        domains=(("d0", "https://d0.example"),),
        auth=OpenSearchAuth.SIGV4,
        region="us-east-1",
    )
    with (
        patch(f"{_MODULE}.botocore.session.get_session", return_value=session),
        patch(f"{_MODULE}.AWSV4SignerAsyncAuth") as signer_cls,
        patch(f"{_MODULE}.AsyncOpenSearch") as client_cls,
    ):
        opensearch_clients_from_config(conn)

    signer_cls.assert_called_once_with(credentials, "us-east-1", "es")
    assert client_cls.call_args.kwargs["http_auth"] is signer_cls.return_value


@pytest.mark.parametrize(
    ("conn", "reason"),
    [
        (OpenSearchConnection(), "at least one domain"),
        (
            OpenSearchConnection(domains=(("d", "http://a"), ("d", "http://b"))),
            "unique",
        ),
        (OpenSearchConnection(domains=(("d", ""),)), "endpoint"),
        (
            OpenSearchConnection(domains=(("d", "http://a"),), timeout_seconds=0),
            "timeout",
        ),
        (
            OpenSearchConnection(domains=(("d", "http://a"),), auth=OpenSearchAuth.SIGV4),
            "region",
        ),
    ],
)
def test_refuses_a_connection_it_cannot_use(
    conn: OpenSearchConnection, reason: str
) -> None:
    """Test that an unusable connection fails at wiring, naming why.

    **Why this test is important:**
      - A misconfigured sink must fail when the process boots, not on the first write of
        a document hours later.

    **What it tests:**
      - No domain, a duplicate name, an empty endpoint, a non-positive timeout, and
        ``sigv4`` without a region each raise ``AppValueError`` naming the reason.
    """
    with pytest.raises(AppValueError, match=reason):
        opensearch_clients_from_config(conn)


def test_sigv4_without_credentials_is_refused() -> None:
    """Test that ``sigv4`` with no AWS credentials fails at wiring.

    **Why this test is important:**
      - A pod whose IRSA role is not mounted would otherwise build a client that fails
        every request with an opaque 403.

    **What it tests:**
      - No credentials from the botocore session raises ``AppValueError``.
    """
    session = MagicMock()
    session.get_credentials.return_value = None
    conn = OpenSearchConnection(
        domains=(("d0", "https://d0.example"),),
        auth=OpenSearchAuth.SIGV4,
        region="us-east-1",
    )
    with (
        patch(f"{_MODULE}.botocore.session.get_session", return_value=session),
        pytest.raises(AppValueError, match="credentials"),
    ):
        opensearch_clients_from_config(conn)
