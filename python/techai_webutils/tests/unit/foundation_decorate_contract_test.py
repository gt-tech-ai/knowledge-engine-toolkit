"""Tests for bases built from a contract (mirrors Go ``foundation_decorate_contract_test.go``)."""

from __future__ import annotations

import contextlib
import dataclasses
from typing import Protocol
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors import AppRuntimeError, AppValueError
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import (
    Collaborators,
    Decorations,
    Observability,
    ops_of,
)
from techai_webutils.repos.repository import RepositoryBase
from techai_webutils.services.service import ServiceBase


class _Docs(Protocol):
    """A repository contract with two operations."""

    async def count(self, org: str) -> int:
        """Count org's documents."""
        ...

    async def find(self, doc_id: str) -> str:
        """Find one document."""
        ...


def _with_repositories(
    overrides: dict[str, decoration.Layers],
) -> decoration.DecorationConfig:
    """Return the default config with the repository tier's overrides replaced."""
    config = decoration.default_config()
    return dataclasses.replace(
        config,
        repositories=dataclasses.replace(config.repositories, overrides=overrides),
    )


@pytest.mark.asyncio
async def test_each_contract_method_takes_its_own_decoration() -> None:
    """A base declares one operation per contract method, each under ``<name>.<op>``.

    Why this test is important:
      - A component's operations are tuned one by one in YAML; sharing the component's
        decoration would make one query impossible to trace or time out on its own.

    What it tests:
      - With logging on for ``api.docs.count`` only, ``count`` is logged and ``find`` is not.
      - Both are listed by ``components`` and count as used for ``verify``.
    """
    logger = MagicMock()
    logged: list[str] = []
    logger.debug.side_effect = lambda _msg, **fields: logged.append(  # pyright: ignore[reportUnknownLambdaType]
        str(fields.get("operation"))
    )
    decorations = Decorations(
        _with_repositories({"api.docs.count": decoration.Layers(logging=True)}),
        Collaborators(observability=Observability(logger=logger)),
    )
    base = RepositoryBase(_Docs, decorations, "api.docs")

    assert await base.exec("count", AsyncMock(return_value=4)) == 4
    assert await base.exec("find", AsyncMock(return_value="d")) == "d"

    assert logged == ["count"]
    assert ("repository", "api.docs.count") in decorations.components()
    assert ("repository", "api.docs.find") in decorations.components()
    decorations.verify("api")


@pytest.mark.asyncio
async def test_an_operation_the_contract_lacks_raises() -> None:
    """Running an undeclared operation raises before its body runs.

    Why this test is important:
      - An operation name the contract does not have has no config key an operator could tune.

    What it tests:
      - ``exec("rename")`` raises ``AppRuntimeError`` naming the component and the operation.
    """
    base = RepositoryBase(_Docs, Decorations(), "api.docs")
    body = AsyncMock(return_value=0)

    with pytest.raises(AppRuntimeError, match=r"api\.docs has no operation rename"):
        await base.exec("rename", body)
    body.assert_not_awaited()


def test_a_contract_must_have_methods() -> None:
    """A contract with no public method is refused at construction.

    Why this test is important:
      - Every call on such a base would fail at run time; the composition root must refuse it.

    What it tests:
      - ``ops_of`` and ``ServiceBase`` raise ``AppRuntimeError`` for an empty class; a
        contract's properties and private methods are not operations.
    """

    class _Empty(Protocol):
        """No operations."""

        @property
        def name(self) -> str:
            """A property, not an operation."""
            ...

        def _helper(self) -> None:
            """Help privately; not an operation."""

    with pytest.raises(AppRuntimeError):
        ops_of(_Empty)
    with pytest.raises(AppRuntimeError):
        ServiceBase(_Empty, Decorations(), "api.docs")
    assert ops_of(_Docs) == ["count", "find"]


def test_verify_fails_an_override_no_contract_declares() -> None:
    """An override naming a missing operation is reported.

    Why this test is important:
      - An override for a renamed operation silently tunes nothing.

    What it tests:
      - ``verify`` raises ``AppValueError`` naming ``api.docs.rename``; an override of a
        declared operation passes.
    """
    stale = Decorations(
        _with_repositories({"api.docs.rename": decoration.Layers(logging=False)})
    )
    RepositoryBase(_Docs, stale, "api.docs")
    with pytest.raises(AppValueError, match=r"api\.docs\.rename"):
        stale.verify("api")

    clean = Decorations(
        _with_repositories({"api.docs.find": decoration.Layers(logging=False)})
    )
    RepositoryBase(_Docs, clean, "api.docs")
    clean.verify("api")


@pytest.mark.asyncio
async def test_operations_share_their_components_breaker() -> None:
    """A tripped component breaker sheds every operation unless one configures its own.

    Why this test is important:
      - A breaker protects one dependency; a breaker per operation would let every operation
        fail on its own before shedding.

    What it tests:
      - After ``count`` trips the component breaker, ``find``'s body does not run; with its own
        breaker override, ``find`` keeps running.
    """
    breaker = decoration.CircuitBreaker(
        enabled=True, consecutive_failures=1, timeout=3600
    )

    async def find_ran(overrides: dict[str, decoration.Layers]) -> bool:
        base = RepositoryBase(
            _Docs, Decorations(_with_repositories(overrides)), "api.docs"
        )

        with pytest.raises(AppError):
            await base.exec(
                "count", AsyncMock(side_effect=AppError(ErrorCode.UNAVAILABLE, "db down"))
            )
        body = AsyncMock(return_value="")
        with contextlib.suppress(AppError):
            await base.exec("find", body)
        return body.await_count == 1

    assert not await find_ran({"api.docs": decoration.Layers(circuit_breaker=breaker)})
    assert await find_ran({
        "api.docs": decoration.Layers(circuit_breaker=breaker),
        "api.docs.find": decoration.Layers(circuit_breaker=breaker),
    })


def test_inventory_lists_each_operation_with_its_sources() -> None:
    """The inventory names the config keys behind each operation's layers.

    Why this test is important:
      - An operator must be able to trace a surprising timeout to the override that set it.

    What it tests:
      - The component and each operation are listed; an operation's sources run from the
        defaults through the component's override to its own.
    """
    decorations = Decorations(
        _with_repositories({
            "api.docs": decoration.Layers(timeout=1.0),
            "api.docs.count": decoration.Layers(timeout=2.0),
        })
    )
    RepositoryBase(_Docs, decorations, "api.docs")

    by_name = {r.name: r for r in decorations.inventory()}
    assert {"api.docs", "api.docs.count", "api.docs.find"} <= set(by_name)
    count = by_name["api.docs.count"]
    assert count.tier == "repository"
    assert count.sources == ["defaults", "overrides.api.docs", "overrides.api.docs.count"]
    assert count.layers is not None
    assert count.layers.timeout == pytest.approx(2.0)
