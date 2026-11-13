"""``source_reader_from_config``: the config-selected ``SourceReader``."""

from __future__ import annotations

import importlib
from pathlib import Path
from typing import TYPE_CHECKING, cast

import httpx

from techai_webutils.clients.source_reader.config import (
    SourceReaderConfig,
    SourceReaderKind,
)
from techai_webutils.clients.source_reader.git.reader import GitSourceReader
from techai_webutils.clients.source_reader.s3.reader import S3SourceReader
from techai_webutils.clients.source_reader.stub.reader import StubSourceReader
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.source_reader.github.reader import (
        GitHubSourceReader as _LazyGitHubSourceReader,
    )
    from techai_webutils.core.interfaces.source_reader import SourceReader
    from techai_webutils.core.interfaces.storage import StorageClient


def source_reader_from_config(
    config: SourceReaderConfig,
    *,
    storage: StorageClient | None = None,
    client: httpx.AsyncClient | None = None,
) -> SourceReader:
    """Build the ``SourceReader`` ``config.kind`` selects.

    Args:
        config: Selects the backend and carries its settings.
        storage: The object-store client the ``s3`` kind reads through (required there).
        client: The HTTP client the ``github`` kind reads through; when omitted, one with
            ``timeout_seconds`` is built (the caller owns closing an injected one).

    Returns:
        The ``SourceReader`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, ``s3`` without ``storage``, ``github``
            without an App id or a readable RSA private key, or ``git`` without
            ``git_root`` (``INVALID_INPUT``).

    """
    if config.kind == SourceReaderKind.STUB:
        return StubSourceReader()
    if config.kind == SourceReaderKind.S3:
        if storage is None:
            msg = "source_reader: the s3 kind requires a storage client"
            raise AppValueError(msg)
        return S3SourceReader(storage)
    if config.kind == SourceReaderKind.GITHUB:
        return _github(config, client)
    if config.kind == SourceReaderKind.GIT:
        if not config.git_root:
            msg = "source_reader: the git kind requires git_root"
            raise AppValueError(msg)
        return GitSourceReader(Path(config.git_root))
    msg = f"unknown source_reader kind: {config.kind!r}"
    raise AppValueError(msg)


def _github(config: SourceReaderConfig, client: httpx.AsyncClient | None) -> SourceReader:
    """Build the ``github`` reader, importing its backend (and ``cryptography``) lazily.

    Args:
        config: Carries the App id, private key, API root and timeouts.
        client: The HTTP client to read through, or ``None`` to build one.

    Returns:
        The ``github`` reader.

    """
    github_cls = cast(
        "type[_LazyGitHubSourceReader]",
        importlib.import_module(
            "techai_webutils.clients.source_reader.github"
        ).GitHubSourceReader,
    )
    if client is None:
        client = httpx.AsyncClient(timeout=config.timeout_seconds)
    return github_cls(client, config)
