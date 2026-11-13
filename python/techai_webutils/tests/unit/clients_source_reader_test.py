"""Unit tests for the source readers (storage and GitHub HTTP mocked)."""

from __future__ import annotations

import base64
import json
from datetime import UTC, datetime
from typing import TYPE_CHECKING, cast, final
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

from techai_webutils.clients.source_reader import (
    SourceReaderConfig,
    SourceReaderKind,
    source_reader_from_config,
)
from techai_webutils.clients.source_reader.decorated import decorate
from techai_webutils.clients.source_reader.github import GitHubSourceReader
from techai_webutils.core.errors import (
    AppError,
    AppTimeoutError,
    AppValueError,
    ErrorCode,
    ForbiddenError,
    InvalidInputError,
    NotFoundError,
    ThrottledError,
    UnauthorizedError,
    UnavailableError,
)
from techai_webutils.core.interfaces.source_reader import SourceReader
from techai_webutils.core.interfaces.storage import StorageClient
from techai_webutils.core.types.source_item import SourceItem
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations
from techai_webutils.foundation.resilience.async_retry import retry_after_s

if TYPE_CHECKING:
    from pathlib import Path


@pytest.mark.asyncio
async def test_factory_defaults_to_stub() -> None:
    """A default config builds the stub reader, which reads empty bytes with no I/O.

    Why this test is important:
        A composition root boots on base config with no infrastructure; the default kind
        must need no storage client, no GitHub App and no network.

    What it tests:
        ``source_reader_from_config(SourceReaderConfig())`` builds with no dependencies,
        and its reader returns ``b""`` for any item.
    """
    reader = source_reader_from_config(SourceReaderConfig())

    assert await reader.read(SourceItem(bucket="b", key="k")) == b""


def test_unknown_kind_fails_loudly() -> None:
    """An unknown kind is refused at build time with ``INVALID_INPUT``.

    Why this test is important:
        A typo in config must stop the deployable at startup, not fall back to a reader
        that silently returns nothing.

    What it tests:
        ``source_reader_from_config`` raises ``AppValueError`` coded ``INVALID_INPUT``
        naming the kind.
    """
    config = SourceReaderConfig(kind=cast("SourceReaderKind", cast("object", "gitlab")))

    with pytest.raises(AppValueError, match="gitlab") as err:
        _ = source_reader_from_config(config)

    assert err.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_s3_reader_reads_bucket_key() -> None:
    """The s3 reader downloads the item's bucket and key through the storage client.

    Why this test is important:
        Every object-store connector's documents reach ingestion through this read; a
        swapped bucket or key would ingest the wrong document or none.

    What it tests:
        The ``s3`` kind awaits ``StorageClient.download(item.bucket, item.key)`` once and
        returns its bytes unchanged.
    """
    storage = AsyncMock(spec=StorageClient)
    cast("MagicMock", storage.download).return_value = b"body"
    reader = source_reader_from_config(
        SourceReaderConfig(kind=SourceReaderKind.S3), storage=storage
    )

    got = await reader.read(SourceItem(bucket="docs", key="org/a.pdf"))

    assert got == b"body"
    cast("MagicMock", cast("AsyncMock", storage.download).assert_awaited_once_with)(
        "docs", "org/a.pdf"
    )


def test_s3_reader_requires_a_storage_client() -> None:
    """The s3 kind without a storage client is refused at build time.

    Why this test is important:
        A composition root that selects ``s3`` but forgets the client must fail at
        startup, not on the first document.

    What it tests:
        ``source_reader_from_config`` raises ``AppValueError`` for ``s3`` with no
        ``storage``.
    """
    with pytest.raises(AppValueError, match="storage"):
        _ = source_reader_from_config(SourceReaderConfig(kind=SourceReaderKind.S3))


_APP_ID = 42
_INSTALLATION = 7
_COMMIT = "0123456789abcdef0123456789abcdef01234567"
_BASE = "https://github.example/api/v3"
_MINT_PATH = f"/api/v3/app/installations/{_INSTALLATION}/access_tokens"
_TOKEN_LIFETIME_S = 3600.0
_MARGIN_S = 300.0
_START = 1_800_000_000.0
_TWO_MINTS = 2
_ITEM = SourceItem(
    owner="acme",
    repo="widgets",
    commit=_COMMIT,
    path="src/a b.go",
    installation_id=_INSTALLATION,
)


@final
class _Clock:
    """A wall clock (Unix seconds) the test steps by hand."""

    def __init__(self) -> None:
        self.now = _START

    def __call__(self) -> float:
        return self.now


@final
class _GitHub:
    """Answers the installation-token exchange and the contents read, recording each."""

    def __init__(self, read: httpx.Response | Exception | None = None) -> None:
        self.requests: list[httpx.Request] = []
        self.mints = 0
        self.read = read

    async def __call__(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        if request.method == "POST" and request.url.path == _MINT_PATH:
            self.mints += 1
            expires = datetime.fromtimestamp(_START + _TOKEN_LIFETIME_S, tz=UTC)
            return httpx.Response(
                201,
                json={
                    "token": f"installation-{self.mints}",
                    "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
                },
            )
        if isinstance(self.read, Exception):
            raise self.read
        return self.read or httpx.Response(200, content=b"package widgets\n")


@pytest.fixture(scope="module")
def app_key() -> rsa.RSAPrivateKey:
    """Generate an RSA key for the test run, standing in for the App's private key.

    Returns:
        A fresh 2048-bit RSA private key.

    """
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


def _pem(key: rsa.RSAPrivateKey) -> str:
    """Return ``key`` as an unencrypted PKCS#8 PEM string.

    Args:
        key: The key to encode.

    Returns:
        ``key`` as an unencrypted PKCS#8 PEM string.

    """
    return key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption(),
    ).decode()


def _github_reader(
    key: rsa.RSAPrivateKey, handler: _GitHub, clock: _Clock
) -> GitHubSourceReader:
    """Build a github reader over a mocked transport answering with ``handler``.

    Args:
        key: The App's private key.
        handler: Answers each request.
        clock: Supplies the wall-clock time.

    Returns:
        A github reader over a mocked transport answering with ``handler``.

    """
    return GitHubSourceReader(
        httpx.AsyncClient(transport=httpx.MockTransport(handler)),
        SourceReaderConfig(
            kind=SourceReaderKind.GITHUB,
            github_app_id=_APP_ID,
            github_private_key=_pem(key),
            github_base_url=_BASE,
            token_refresh_margin_seconds=_MARGIN_S,
        ),
        clock=clock,
    )


def _b64(part: str) -> bytes:
    """Decode one unpadded base64url JWT segment.

    Args:
        part: The segment.

    Returns:
        The decoded bytes.

    """
    return base64.urlsafe_b64decode(part + "=" * (-len(part) % 4))


@pytest.mark.asyncio
async def test_blob_read_at_commit(app_key: rsa.RSAPrivateKey) -> None:
    """The github reader fetches the file's raw bytes at the item's commit.

    Why this test is important:
        A codebase document must be the file as of the commit its sync listed, not
        whatever the branch holds when the read runs, and it must be read as the App
        installation with a JWT GitHub will accept.

    What it tests:
        - The App JWT is RS256 over ``{"iss": app id}``, verifies with the App's public
          key, and is exchanged at ``POST /app/installations/{id}/access_tokens``.
        - The read is ``GET /repos/{owner}/{repo}/contents/{path}?ref={commit}`` with the
          path percent-encoded, the raw media type, and the installation token.
        - The response bytes come back unchanged.
    """
    github = _GitHub()
    reader = _github_reader(app_key, github, _Clock())

    body = await reader.read(_ITEM)

    assert body == b"package widgets\n"
    mint, read = github.requests
    header, claims, signature = (
        mint.headers["Authorization"].removeprefix("Bearer ").split(".")
    )
    app_key.public_key().verify(
        _b64(signature),
        f"{header}.{claims}".encode(),
        padding.PKCS1v15(),
        hashes.SHA256(),
    )
    assert json.loads(_b64(header)) == {"alg": "RS256", "typ": "JWT"}
    assert json.loads(_b64(claims))["iss"] == str(_APP_ID)
    assert read.url.raw_path.startswith(
        b"/api/v3/repos/acme/widgets/contents/src/a%20b.go?"
    )
    assert read.url.params["ref"] == _COMMIT
    assert read.headers["Accept"] == "application/vnd.github.raw+json"
    assert read.headers["Authorization"] == "Bearer installation-1"


@pytest.mark.asyncio
async def test_token_reused_until_five_minutes_before_expiry(
    app_key: rsa.RSAPrivateKey,
) -> None:
    """An installation token is reused while more than the margin remains.

    Why this test is important:
        Minting a token per file would spend the App's rate limit on exchanges and add a
        round trip to every read of a large repository.

    What it tests:
        A read just over five minutes before the token's expiry sends the first token and
        mints no second one.
    """
    github, clock = _GitHub(), _Clock()
    reader = _github_reader(app_key, github, clock)
    _ = await reader.read(_ITEM)

    clock.now = _START + _TOKEN_LIFETIME_S - _MARGIN_S - 1
    _ = await reader.read(_ITEM)

    assert github.mints == 1
    assert github.requests[-1].headers["Authorization"] == "Bearer installation-1"


@pytest.mark.asyncio
async def test_token_refreshed_near_expiry(app_key: rsa.RSAPrivateKey) -> None:
    """An installation token within five minutes of expiry is replaced before the read.

    Why this test is important:
        A token that expires mid-read fails the document with a 401 that no retry
        fixes; refreshing early keeps a long sync reading.

    What it tests:
        A read inside the five-minute margin mints a second token and sends it.
    """
    github, clock = _GitHub(), _Clock()
    reader = _github_reader(app_key, github, clock)
    _ = await reader.read(_ITEM)

    clock.now = _START + _TOKEN_LIFETIME_S - _MARGIN_S + 1
    _ = await reader.read(_ITEM)

    assert github.mints == _TWO_MINTS
    assert github.requests[-1].headers["Authorization"] == "Bearer installation-2"


async def _read_failure(
    read: httpx.Response | Exception, app_key: rsa.RSAPrivateKey
) -> AppError:
    """Read ``_ITEM`` against a GitHub whose contents read answers ``read``.

    Args:
        read: The contents response, or the transport error raised in its place.
        app_key: The App's private key.

    Returns:
        The coded error the read raised.

    """
    reader = _github_reader(app_key, _GitHub(read), _Clock())
    with pytest.raises(AppError) as err:
        _ = await reader.read(_ITEM)
    return err.value


@pytest.mark.asyncio
async def test_5xx_is_transient(app_key: rsa.RSAPrivateKey) -> None:
    """A GitHub 5xx is ``UNAVAILABLE``, so the client tier retries it.

    Why this test is important:
        GitHub's API sheds load with 502s and 503s during incidents; treating them as
        permanent would fail every document of a sync that one retry would have read.

    What it tests:
        A 502 on the contents read raises ``UnavailableError``, which is transient.
    """
    err = await _read_failure(httpx.Response(502), app_key)

    assert isinstance(err, UnavailableError)
    assert err.is_transient


@pytest.mark.asyncio
async def test_secondary_rate_limit_carries_retry_after(
    app_key: rsa.RSAPrivateKey,
) -> None:
    """A secondary rate limit is a throttle carrying GitHub's ``Retry-After``.

    Why this test is important:
        GitHub answers a secondary rate limit with a 403, which reads as a permanent
        refusal; retrying sooner than ``Retry-After`` extends the ban, so the retry layer
        must see both the throttle and the wait.

    What it tests:
        A 403 with ``Retry-After: 60`` and a secondary-rate-limit body raises
        ``ThrottledError`` whose retry pushback is 60 seconds.
    """
    response = httpx.Response(
        403,
        headers={"Retry-After": "60"},
        json={"message": "You have exceeded a secondary rate limit."},
    )

    err = await _read_failure(response, app_key)

    assert isinstance(err, ThrottledError)
    assert retry_after_s(err) == pytest.approx(60)


@pytest.mark.asyncio
async def test_404_is_not_found(app_key: rsa.RSAPrivateKey) -> None:
    """A 404 is ``NOT_FOUND``, which no retry fixes.

    Why this test is important:
        A file deleted, or a commit force-pushed away, between listing and reading must
        fail that one document permanently instead of burning the retry budget.

    What it tests:
        A 404 on the contents read raises ``NotFoundError``, which is not transient.
    """
    err = await _read_failure(httpx.Response(404), app_key)

    assert isinstance(err, NotFoundError)
    assert not err.is_transient


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("read", "expected"),
    [
        (httpx.Response(401), UnauthorizedError),
        (
            httpx.Response(403, json={"message": "Resource not accessible"}),
            ForbiddenError,
        ),
        (
            httpx.Response(
                403,
                headers={"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800000030"},
            ),
            ThrottledError,
        ),
        (httpx.Response(429), ThrottledError),
        (httpx.Response(422), AppError),
        (httpx.ReadTimeout("slow"), AppTimeoutError),
        (httpx.ConnectError("refused"), UnavailableError),
    ],
)
async def test_other_failures_are_coded(
    read: httpx.Response | Exception,
    expected: type[AppError],
    app_key: rsa.RSAPrivateKey,
) -> None:
    """Every other GitHub failure reaches the caller as its coded error.

    Why this test is important:
        Retryability, the breaker and the transport status all derive from the code; an
        auth failure retried, or a timeout treated as permanent, misroutes the failure.

    What it tests:
        401 is ``UNAUTHENTICATED``; a plain 403 is ``FORBIDDEN``; a primary rate limit
        (``X-RateLimit-Remaining: 0``) and a 429 are throttles; any other status is
        ``UPSTREAM``; a transport timeout is ``TIMEOUT`` and a refused connection
        ``UNAVAILABLE``.
    """
    err = await _read_failure(read, app_key)

    assert type(err) is expected
    if expected is AppError:
        assert err.code is ErrorCode.UPSTREAM


@pytest.mark.asyncio
async def test_primary_rate_limit_waits_until_reset(app_key: rsa.RSAPrivateKey) -> None:
    """A spent primary rate limit pushes the retry back until ``X-RateLimit-Reset``.

    Why this test is important:
        Retrying before the hourly window resets only spends attempts on more 403s.

    What it tests:
        ``X-RateLimit-Remaining: 0`` with a reset 30 s after now yields a 30 s pushback.
    """
    response = httpx.Response(
        403,
        headers={
            "X-RateLimit-Remaining": "0",
            "X-RateLimit-Reset": str(int(_START) + 30),
        },
    )

    err = await _read_failure(response, app_key)

    assert retry_after_s(err) == pytest.approx(30)


@pytest.mark.asyncio
async def test_decorated_reader_forwards_read() -> None:
    """The decorated reader runs ``read`` through the client tier to the wrapped reader.

    Why this test is important:
        The composition root wraps the reader in timeout, retry and breaker; a wrapper
        that dropped the item would read another document or none.

    What it tests:
        ``decorate(...).read(item)`` awaits the wrapped reader once with the same item and
        returns its bytes.
    """
    inner = AsyncMock(spec=SourceReader)
    cast("MagicMock", inner.read).return_value = b"body"
    reader = decorate(
        inner, Decorations(decoration.default_config(), Collaborators()), "source_reader"
    )

    assert await reader.read(_ITEM) == b"body"
    cast("MagicMock", cast("AsyncMock", inner.read).assert_awaited_once_with)(_ITEM)


def _git_reader(root: Path) -> SourceReader:
    """Build the ``git`` reader over ``root`` through the factory.

    Args:
        root: The work tree's directory.

    Returns:
        The ``git`` reader.

    """
    return source_reader_from_config(
        SourceReaderConfig(kind=SourceReaderKind.GIT, git_root=str(root))
    )


@pytest.mark.asyncio
async def test_git_reader_reads_key(tmp_path: Path) -> None:
    """The git reader reads the item's path under the work tree root.

    Why this test is important:
        The ingestion worker reads each listed file's body through this reader; a wrong
        join (or reading ``key`` instead of ``path``) would ingest nothing or the wrong
        file, and a missing file must be a terminal ``NOT_FOUND``, not a crash.

    What it tests:
        ``docs/a.md`` under the root reads back its bytes; ``docs/gone.md`` raises
        ``NotFoundError``.
    """
    (tmp_path / "docs").mkdir()
    _ = (tmp_path / "docs" / "a.md").write_bytes(b"# A\n")
    reader = _git_reader(tmp_path)

    assert await reader.read(SourceItem(path="docs/a.md")) == b"# A\n"
    with pytest.raises(NotFoundError):
        _ = await reader.read(SourceItem(path="docs/gone.md"))


@pytest.mark.asyncio
@pytest.mark.parametrize("path", ["../outside.txt", "docs/../../outside.txt", "", "/x"])
async def test_git_reader_refuses_traversal(tmp_path: Path, path: str) -> None:
    """The git reader refuses a path that is empty, absolute or climbs out of the root.

    Why this test is important:
        An item's path comes from a queue message; a crafted ``..`` or absolute path
        must never read a file beside the work tree (a secrets file, another checkout).

    What it tests:
        With a real file just outside the root, each of ``../outside.txt``,
        ``docs/../../outside.txt``, ``""`` and an absolute path raises
        ``InvalidInputError`` coded ``INVALID_INPUT``.
    """
    root = tmp_path / "root"
    (root / "docs").mkdir(parents=True)
    _ = (tmp_path / "outside.txt").write_bytes(b"secret")
    absolute = str(tmp_path / "outside.txt") if path == "/x" else path

    with pytest.raises(InvalidInputError) as err:
        _ = await _git_reader(root).read(SourceItem(path=absolute))

    assert err.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_git_reader_refuses_symlink_out_of_root(tmp_path: Path) -> None:
    """The git reader refuses a symlink in the root that points outside it.

    Why this test is important:
        A link committed to the tree is a path, not content; following it out of the
        root would read whatever it names on the host.

    What it tests:
        ``link.md`` → a file outside the root raises ``InvalidInputError``.
    """
    root = tmp_path / "root"
    root.mkdir()
    _ = (tmp_path / "outside.txt").write_bytes(b"secret")
    (root / "link.md").symlink_to(tmp_path / "outside.txt")

    with pytest.raises(InvalidInputError):
        _ = await _git_reader(root).read(SourceItem(path="link.md"))


def test_git_reader_without_root_fails_loudly() -> None:
    """The ``git`` kind with no root is refused at build time.

    Why this test is important:
        An empty root would read relative to the process's working directory; the
        deployable must stop at startup instead.

    What it tests:
        ``source_reader_from_config(kind=git)`` with no ``git_root`` raises
        ``AppValueError`` coded ``INVALID_INPUT``.
    """
    with pytest.raises(AppValueError, match="git_root") as err:
        _ = source_reader_from_config(SourceReaderConfig(kind=SourceReaderKind.GIT))

    assert err.value.code is ErrorCode.INVALID_INPUT
