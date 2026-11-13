"""``GitHubSourceReader``: a file's bytes at a commit, read as a GitHub App installation.

The reader signs a short-lived RS256 App JWT, exchanges it for an installation token
(``POST /app/installations/{id}/access_tokens``), caches that token per installation until
``refresh_margin_seconds`` before it expires, and reads the file through the contents API
with the raw media type, pinned to the item's commit.
"""

from __future__ import annotations

import asyncio
import base64
import json
import time
from datetime import datetime
from http import HTTPStatus
from typing import TYPE_CHECKING, cast, final, override
from urllib.parse import quote

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

from techai_webutils.clients.source_reader.github.classify import raise_for, send
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.source_reader import SourceReader

if TYPE_CHECKING:
    from collections.abc import Callable

    import httpx

    from techai_webutils.clients.source_reader.config import SourceReaderConfig
    from techai_webutils.core.types.source_item import SourceItem

_API_VERSION = "2022-11-28"
_JWT_BACKDATE_S = 60
"""Seconds the JWT's ``iat`` is backdated, tolerating clock drift (GitHub's guidance)."""
_JWT_LIFETIME_S = 540
"""Seconds the JWT is valid past now; GitHub caps it at ten minutes."""


def _b64url(raw: bytes) -> str:
    """Return ``raw`` as unpadded base64url, the JWT segment encoding.

    Args:
        raw: The bytes to encode.

    Returns:
        ``raw`` as unpadded base64url text.

    """
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def _load_rsa_key(pem: str) -> rsa.RSAPrivateKey:
    """Parse the App's PEM private key, which must be RSA.

    Args:
        pem: The unencrypted PEM private key.

    Returns:
        The parsed RSA private key.

    Raises:
        AppValueError: The PEM does not parse, or is not an RSA key (``INVALID_INPUT``).

    """
    try:
        key = serialization.load_pem_private_key(pem.encode(), password=None)
    except (ValueError, TypeError) as err:
        msg = "source_reader: the github App private key is not a readable PEM key"
        raise AppValueError(msg) from err
    if not isinstance(key, rsa.RSAPrivateKey):
        msg = "source_reader: the github App private key must be an RSA key"
        raise AppValueError(msg)
    return key


@final
class GitHubSourceReader(SourceReader):
    """Reads ``owner``/``repo`` ``path`` at ``commit`` as an App installation."""

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: SourceReaderConfig,
        *,
        clock: Callable[[], float] = time.time,
    ) -> None:
        """Hold the HTTP client and parse the App key; no I/O.

        Args:
            client: The HTTP client every request goes through; it owns the timeout.
            config: Carries ``github_base_url``, ``github_app_id``,
                ``github_private_key`` and ``token_refresh_margin_seconds``.
            clock: The wall clock in Unix seconds; injectable for tests.

        Raises:
            AppValueError: ``github_app_id`` is not positive, or the key is not a
                readable RSA PEM key (``INVALID_INPUT``).

        """
        if config.github_app_id <= 0:
            msg = "source_reader: the github kind requires github_app_id"
            raise AppValueError(msg)
        self._client = client
        self._base = config.github_base_url.rstrip("/")
        self._app_id = config.github_app_id
        self._key = _load_rsa_key(config.github_private_key)
        self._margin = config.token_refresh_margin_seconds
        self._clock = clock
        self._tokens: dict[int, tuple[str, float]] = {}
        self._lock = asyncio.Lock()

    @override
    async def read(self, item: SourceItem) -> bytes:
        """Return the file's raw bytes at ``item.commit``.

        An item missing a github field is refused with ``INVALID_INPUT`` before any
        request.

        Args:
            item: Names ``owner``, ``repo``, ``commit``, ``path`` and
                ``installation_id``.

        Returns:
            The file's bytes, unchanged.

        """
        _require_github_fields(item)
        token = await self._token(item.installation_id)
        url = (
            f"{self._base}/repos/{quote(item.owner, safe='')}/"
            f"{quote(item.repo, safe='')}/contents/{quote(item.path)}"
        )
        response = await send(
            self._client.get(
                url,
                params={"ref": item.commit},
                headers=self._headers(
                    f"Bearer {token}", "application/vnd.github.raw+json"
                ),
            )
        )
        raise_for(response, expected=HTTPStatus.OK, now=self._clock())
        return response.content

    async def _token(self, installation_id: int) -> str:
        """Return a cached installation token, minting one inside the refresh margin.

        Args:
            installation_id: The App installation to act as.

        Returns:
            An installation token with more than the margin left.

        """
        async with self._lock:
            cached = self._tokens.get(installation_id)
            if cached is not None and cached[1] - self._clock() > self._margin:
                return cached[0]
            response = await send(
                self._client.post(
                    f"{self._base}/app/installations/{installation_id}/access_tokens",
                    headers=self._headers(
                        f"Bearer {self._jwt()}", "application/vnd.github+json"
                    ),
                )
            )
            raise_for(response, expected=HTTPStatus.CREATED, now=self._clock())
            token, expires_at = _parse_token(response)
            self._tokens[installation_id] = (token, expires_at)
            return token

    def _jwt(self) -> str:
        """Return a fresh RS256 App JWT issued by the App id.

        Returns:
            The signed compact JWT.

        """
        now = int(self._clock())
        header = _b64url(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
        claims = _b64url(
            json.dumps({
                "iat": now - _JWT_BACKDATE_S,
                "exp": now + _JWT_LIFETIME_S,
                "iss": str(self._app_id),
            }).encode()
        )
        signing_input = f"{header}.{claims}".encode()
        signature = self._key.sign(signing_input, padding.PKCS1v15(), hashes.SHA256())
        return f"{header}.{claims}.{_b64url(signature)}"

    @staticmethod
    def _headers(authorization: str, accept: str) -> dict[str, str]:
        """Return the headers every GitHub REST request carries.

        Args:
            authorization: The ``Authorization`` value.
            accept: The ``Accept`` media type.

        Returns:
            The request headers.

        """
        return {
            "Accept": accept,
            "Authorization": authorization,
            "X-GitHub-Api-Version": _API_VERSION,
        }


def _require_github_fields(item: SourceItem) -> None:
    """Refuse an item missing a field the github read needs.

    Args:
        item: The item to check.

    Raises:
        AppValueError: ``owner``, ``repo``, ``commit`` or ``path`` is empty, or
            ``installation_id`` is not positive (``INVALID_INPUT``).

    """
    missing = [
        name
        for name, value in (
            ("owner", item.owner),
            ("repo", item.repo),
            ("commit", item.commit),
            ("path", item.path),
        )
        if not value
    ]
    if item.installation_id <= 0:
        missing.append("installation_id")
    if missing:
        msg = f"source_reader: a github item needs {', '.join(missing)}"
        raise AppValueError(msg)


def _parse_token(response: httpx.Response) -> tuple[str, float]:
    """Return the installation token and its expiry (Unix seconds) from a 201.

    Args:
        response: The access-token exchange's response.

    Returns:
        The token and its expiry.

    Raises:
        AppError: The body lacks a token or a readable ``expires_at`` (``UPSTREAM``).

    """
    try:
        body = cast("dict[str, object]", response.json())
        token = str(body["token"])
        expires_at = datetime.fromisoformat(str(body["expires_at"])).timestamp()
    except (ValueError, KeyError, TypeError) as err:
        raise AppError(
            ErrorCode.UPSTREAM,
            "github: the installation-token response is malformed",
            cause=err,
        ) from err
    return token, expires_at
