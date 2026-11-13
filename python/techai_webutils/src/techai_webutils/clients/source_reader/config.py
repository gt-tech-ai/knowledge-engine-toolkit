"""``SourceReaderKind`` and ``SourceReaderConfig``: the reader and its settings."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum


class SourceReaderKind(StrEnum):
    """Which source reader to build."""

    STUB = "stub"
    """Reads every item as an empty body, with no I/O (the default)."""
    S3 = "s3"
    """Downloads ``bucket`` + ``key`` through the injected ``StorageClient``."""
    GITHUB = "github"
    """Reads ``path`` at ``commit`` from ``owner``/``repo`` as a GitHub App installation
    (needs the ``github`` extra)."""


@dataclass(frozen=True, slots=True)
class SourceReaderConfig:
    """Selects and configures the source reader."""

    kind: SourceReaderKind = SourceReaderKind.STUB
    """The reader to build; ``stub`` by default."""
    github_app_id: int = 0
    """The GitHub App's id, the JWT issuer (``github`` kind; required there)."""
    github_private_key: str = field(default="", repr=False)
    """The App's unencrypted PEM RSA private key (``github`` kind; required there)."""
    github_base_url: str = "https://api.github.com"
    """The REST API root; a GitHub Enterprise Server's is ``https://<host>/api/v3``."""
    timeout_seconds: float = 30.0
    """Per-request timeout of the HTTP client the factory builds (``github`` kind)."""
    token_refresh_margin_seconds: float = 300.0
    """Mint a new installation token once less than this remains on the cached one."""
