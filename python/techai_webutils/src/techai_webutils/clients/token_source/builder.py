"""``new_token_source_from_config``: the config-selected ``TokenSource``."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

import httpx

from techai_webutils.clients.token_source.oauth.source import OAuthTokenSource
from techai_webutils.clients.token_source.static.source import StaticTokenSource
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.token_source import TokenSource


class TokenSourceKind(StrEnum):
    """Which token source to build."""

    STATIC = "static"
    """Hands out the configured ``token`` with no I/O (the default; empty when unset)."""
    OAUTH = "oauth"
    """Mints tokens from an OAuth 2.0 token endpoint and refreshes them before expiry."""


@dataclass(frozen=True, slots=True)
class TokenSourceConfig:
    """Selects and configures the token source.

    The ``oauth`` grant form carries ``grant_type`` and every non-empty credential field below,
    so one shape covers the client-credentials, password and Auth0 ``password-realm`` grants.
    """

    kind: TokenSourceKind = TokenSourceKind.STATIC
    """The source to build; ``static`` by default."""
    token: str = field(default="", repr=False)
    """The ``static`` kind's token."""
    token_url: str = ""
    """The ``oauth`` token endpoint (required there), e.g. ``https://<tenant>/oauth/token``."""
    grant_type: str = "client_credentials"
    """The OAuth grant, e.g. ``client_credentials`` or ``password``."""
    client_id: str = ""
    """The OAuth client id."""
    client_secret: str = field(default="", repr=False)
    """The OAuth client secret."""
    audience: str = ""
    """The API the token is for (an Auth0 extension parameter)."""
    username: str = ""
    """The resource owner, for a password grant."""
    password: str = field(default="", repr=False)
    """The resource owner's password, for a password grant."""
    realm: str = ""
    """The user store, for Auth0's ``password-realm`` grant."""
    scope: str = ""
    """Space-separated scopes to request."""
    refresh_margin_seconds: float = 60.0
    """Mint a new token once the cached one expires within this many seconds."""
    timeout_seconds: float = 10.0
    """Bounds each token request, body included."""

    def grant(self) -> dict[str, str]:
        """Return the grant form: ``grant_type`` plus every non-empty credential field."""
        fields = {
            "client_id": self.client_id,
            "client_secret": self.client_secret,
            "audience": self.audience,
            "username": self.username,
            "password": self.password,
            "realm": self.realm,
            "scope": self.scope,
        }
        return {"grant_type": self.grant_type} | {k: v for k, v in fields.items() if v}


def new_token_source_from_config(
    config: TokenSourceConfig, *, client: httpx.AsyncClient | None = None
) -> TokenSource:
    """Build the ``TokenSource`` ``config.kind`` selects.

    The ``oauth`` kind posts through ``client`` when given (tests, a shared pool), else through a
    new ``httpx.AsyncClient`` bounded by ``timeout_seconds``.

    Raises:
        AppValueError: An unknown kind, or ``oauth`` without a ``token_url`` (``INVALID_INPUT``).

    """
    if config.kind == TokenSourceKind.STATIC:
        return StaticTokenSource(config.token)
    if config.kind == TokenSourceKind.OAUTH:
        if not config.token_url:
            msg = "token_source: token_url is required for the oauth kind"
            raise AppValueError(msg)
        http = client or httpx.AsyncClient(timeout=config.timeout_seconds)
        return OAuthTokenSource(
            http,
            config.token_url,
            config.grant(),
            refresh_margin_seconds=config.refresh_margin_seconds,
        )
    msg = f"unknown token_source kind: {config.kind!r}"
    raise AppValueError(msg)
