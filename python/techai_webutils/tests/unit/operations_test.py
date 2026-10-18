"""Tests for the tier bases' named operations (mirrors Go ``operations_test.go``)."""

from __future__ import annotations

import dataclasses
from typing import TYPE_CHECKING
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import InvalidInputError
from techai_webutils.core.interfaces.repository import (
    TransactionManager,
    in_transaction,
)
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import (
    Collaborators,
    Decorations,
    Deps,
    Observability,
)
from techai_webutils.repos.repository import RepositoryBase
from techai_webutils.repos.repository import tier as repository_tier
from techai_webutils.services.service import ServiceBase
from techai_webutils.workflows.workflow import WorkflowBase

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.op import Op


def _with_repositories(
    overrides: dict[str, decoration.Layers],
) -> decoration.DecorationConfig:
    """Return the default config with the repository tier's overrides replaced."""
    config = decoration.default_config()
    return dataclasses.replace(
        config,
        repositories=dataclasses.replace(config.repositories, overrides=overrides),
    )


def _counting_retrier() -> tuple[MagicMock, list[int]]:
    """Return a retrier that runs its operation once and counts its calls."""
    calls: list[int] = []
    retrier = MagicMock()

    async def retry(fn: object) -> object:
        calls.append(1)
        return await fn()  # type: ignore[operator]

    retrier.retry.side_effect = retry
    return retrier, calls


@pytest.mark.asyncio
async def test_each_operation_takes_its_own_decoration_name() -> None:
    """A repository query is decorated under ``<repository>.<op>``.

    Why this test is important:
      - A component's operations are tuned one by one in YAML; sharing the component's
        decoration would make one query impossible to trace or time out on its own.

    What it tests:
      - With logging on for ``api.docs.count`` only, ``count`` is logged and ``find`` is not.
      - Both are listed by ``components`` and count as used for ``verify``.
    """
    logger = MagicMock()
    logged: list[str] = []
    logger.debug.side_effect = lambda _msg, **fields: logged.extend(  # pyright: ignore[reportUnknownLambdaType]
        str(v) for k, v in fields.items() if k != "operation"
    )
    decorations = Decorations(
        _with_repositories({"api.docs.count": decoration.Layers(logging=True)}),
        Collaborators(observability=Observability(logger=logger)),
    )
    base = RepositoryBase.from_decorations("api.docs", decorations)

    async def count(org: str) -> int:  # noqa: RUF029 - an operation is an async function
        return len(org)

    async def find(doc_id: str) -> str:  # noqa: RUF029 - an operation is an async function
        return doc_id

    count_op: Op[str, int] = base.query("count", count)
    assert await count_op.execute("org1") == 4
    assert await base.query("find", find).execute("d1") == "d1"

    assert "api.docs.count" in logged
    assert "api.docs.find" not in logged
    assert ("repository", "api.docs.count") in decorations.components()
    assert ("repository", "api.docs.find") in decorations.components()
    decorations.verify("api")


@pytest.mark.asyncio
async def test_a_repository_command_never_retries() -> None:
    """A repository command runs without the retrier; a query runs through it.

    Why this test is important:
      - A retried write whose first attempt committed writes twice.

    What it tests:
      - One retrier call for one query and one command.
    """
    retrier, calls = _counting_retrier()
    base = RepositoryBase("api.docs", Deps(retrier=retrier))

    async def echo(value: str) -> str:  # noqa: RUF029 - an operation is an async function
        return value

    await base.query("find", echo).execute("d1")
    await base.command("rename", echo).execute("d1")
    assert len(calls) == 1


@dataclasses.dataclass(frozen=True)
class _Rename:
    """A service input that validates itself."""

    name: str
    """The new name; empty is invalid."""

    def validate(self) -> None:
        """Reject an empty name."""
        if not self.name:
            msg = "name is required"
            raise InvalidInputError(msg)


@pytest.mark.asyncio
async def test_a_service_operation_validates_its_input() -> None:
    """A service operation validates an input that has ``validate()`` before it runs.

    Why this test is important:
      - An invalid input that reached the repository would persist bad data.

    What it tests:
      - An invalid input raises its coded error and the operation does not run; a valid one runs.
    """
    ran: list[str] = []
    base = ServiceBase.from_decorations("api.docs", Decorations())

    async def rename(value: _Rename) -> str:  # noqa: RUF029 - an operation is an async function
        ran.append(value.name)
        return value.name

    op = base.op("rename", rename)
    with pytest.raises(InvalidInputError):
        await op.execute(_Rename(""))
    assert await op.execute(_Rename("x")) == "x"
    assert ran == ["x"]


@pytest.mark.asyncio
async def test_workflow_read_saga_and_command_own_their_transactions() -> None:
    """Read and saga open no transaction; command opens one.

    Why this test is important:
      - The workflow is the only tier that opens a transaction; a command that did not would
        have its writes refused, and a read that did would hold a connection for nothing.

    What it tests:
      - ``read`` and ``saga`` run with ``in_transaction()`` false, ``command`` with it true.
    """
    tx = MagicMock(spec=TransactionManager)

    async def with_transaction(fn: object) -> object:
        return await fn()  # type: ignore[operator]

    tx.with_transaction.side_effect = with_transaction
    base = WorkflowBase.from_decorations("identity.team", Decorations(), tx)

    async def probe(_: str) -> bool:  # noqa: RUF029 - an operation is an async function
        return in_transaction()

    assert await base.read("get", probe).execute("t") is False
    assert await base.saga("provision", probe).execute("t") is False
    assert await base.command("create", probe).execute("t") is True


@pytest.mark.asyncio
async def test_client_call_once_never_retries() -> None:
    """A client call runs through the retrier; ``call_once`` does not.

    Why this test is important:
      - An outbound send must never be replayed, while an idempotent read should be.

    What it tests:
      - One retrier call for one ``call`` and one ``call_once``, each returning its result.
    """
    retrier, calls = _counting_retrier()
    base = ClientBase("cli.sts", Deps(retrier=retrier))

    async def caller(account: str) -> str:  # noqa: RUF029 - an operation is an async function
        return f"caller:{account}"

    assert await base.call("get_caller_identity", caller).execute("a") == "caller:a"
    assert await base.call_once("send_email", caller).execute("a") == "caller:a"
    assert len(calls) == 1


def test_verify_explicit_fails_on_a_component_with_no_entry() -> None:
    """``verify_explicit`` names a resolved component that has no override of its own name.

    Why this test is important:
      - A generated decoration file lists every component; one added without its entry takes
        only the defaults, the drift the generator exists to prevent.

    What it tests:
      - It fails naming ``api.users`` and ignores ``identity.users``; with an entry for each,
        it passes.
    """
    off = decoration.Layers(logging=False)
    decorations = Decorations(_with_repositories({"api.docs": off}))
    for name in ("api.docs", "api.users", "identity.users"):
        decorations.deps(repository_tier(), name)
    with pytest.raises(AppValueError) as raised:
        decorations.verify_explicit("api")
    assert "repositories.overrides.api.users" in str(raised.value)
    assert "identity.users" not in str(raised.value)

    clean = Decorations(_with_repositories({"api.docs": off, "api.users": off}))
    for name in ("api.docs", "api.users"):
        clean.deps(repository_tier(), name)
    clean.verify_explicit("api")
