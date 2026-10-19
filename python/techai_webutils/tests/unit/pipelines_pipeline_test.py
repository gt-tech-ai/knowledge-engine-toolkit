"""Tests for the pipeline tier: ``PipelineBase`` builds decorated stages (Go ``pipelines/pipeline``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Protocol
from unittest.mock import AsyncMock, MagicMock, call

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import ConflictError, InternalError
from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.foundation.decorate import Decorations, Deps, Layer
from techai_webutils.pipelines import PipelineBase, tier

if TYPE_CHECKING:
    from conftest import Observed


class _Stages(Protocol):
    """The test pipeline's contract: one operation per stage tested."""

    async def parse(self, x: int) -> int:
        """Parse."""
        ...

    async def enrich(self, x: int) -> int:
        """Enrich."""
        ...

    async def a(self, x: int) -> int:
        """Stage a."""
        ...

    async def b(self, x: int) -> int:
        """Stage b."""
        ...

    async def c(self, x: int) -> int:
        """Stage c."""
        ...

    async def both(self, x: int) -> list[int]:
        """Fan out a and b."""
        ...

    async def f(self, x: int) -> list[int]:
        """Fan out x."""
        ...

    async def x(self, x: int) -> int:
        """Stage x."""
        ...

    async def s(self, x: int) -> int:
        """Stage s."""
        ...


def test_tier_order_matches_go() -> None:
    """The pipeline tier's layers, outermost first, are Go's.

    Why this test is important:
      - Both languages must decorate a stage the same way.

    What it tests:
      - recovery → logging → tracing → metrics → timeout.
    """
    assert tier().order == (
        Layer.RECOVERY,
        Layer.LOGGING,
        Layer.TRACING,
        Layer.METRICS,
        Layer.TIMEOUT,
    )


@pytest.mark.asyncio
async def test_then_chains_decorated_stages(observed: Observed) -> None:
    """Chained stages feed each other and each is observed under its own name.

    Why this test is important:
      - A pipeline's latency breakdown needs one span per stage.

    What it tests:
      - ``parse.then(enrich)`` returns enrich(parse(x)), opens ``p.parse`` then ``p.enrich``,
        and counts each stage once under the pipeline's series.
    """
    base = PipelineBase(
        _Stages, Decorations.fixed(Deps(observability=observed.observability)), "p"
    )
    parse = base.stage("parse", AsyncMock(side_effect=lambda x: x + 1))
    enrich = base.stage("enrich", AsyncMock(side_effect=lambda x: x * 10))

    assert await parse.then(enrich).execute(1) == 20
    assert [name for name, _, _ in observed.spans] == ["p.parse", "p.enrich"]
    assert observed.instruments["pipeline_operations_total"].inc.call_args_list == [
        call(1.0, pipeline="p", operation="parse"),
        call(1.0, pipeline="p", operation="enrich"),
    ]


@pytest.mark.asyncio
async def test_a_failing_stage_stops_the_chain() -> None:
    """A stage's coded error propagates unchanged and later stages do not run.

    Why this test is important:
      - A failed parse must not feed garbage downstream, and callers branch on the code.

    What it tests:
      - ``ConflictError`` from the first stage propagates; the second is never awaited; an
        uncoded error is coded INTERNAL by recovery.
    """
    base = PipelineBase(_Stages, Decorations(), "p")
    later = AsyncMock()
    failing = base.stage("a", AsyncMock(side_effect=ConflictError("dup")))

    with pytest.raises(ConflictError):
        await failing.then(base.stage("b", later)).execute(1)
    later.assert_not_awaited()
    with pytest.raises(InternalError):
        await base.stage("c", AsyncMock(side_effect=KeyError("k"))).execute(1)


@pytest.mark.asyncio
async def test_fan_out_runs_stages_concurrently_in_order() -> None:
    """``fan_out`` runs its stages over one input concurrently, keeping their order.

    Why this test is important:
      - Independent lookups must overlap, and callers index the outputs by position.

    What it tests:
      - Two stages that each wait for the other both finish, and the outputs keep the
        stages' order; a failing stage fails the fan-out with its own coded error.
    """
    base = PipelineBase(_Stages, Decorations(), "p")
    both = asyncio.Barrier(2)

    async def first(x: int) -> str:
        await both.wait()
        return f"a{x}"

    async def second(x: int) -> str:
        await both.wait()
        return f"b{x}"

    fan = base.fan_out("both", base.stage("a", first), base.stage("b", second))
    assert await asyncio.wait_for(fan.execute(1), timeout=1) == ["a1", "b1"]

    failing = base.fan_out(
        "f", base.stage("x", AsyncMock(side_effect=ConflictError("dup")))
    )
    with pytest.raises(ConflictError):
        await failing.execute(1)


def test_wiring_bugs_fail_loud() -> None:
    """A missing stage function or chained stage is a coded wiring error.

    Why this test is important:
      - A wiring bug must fail at the composition root, not on the first request.

    What it tests:
      - ``stage`` with None and ``then`` with None raise ``AppValueError``.
    """
    base = PipelineBase(_Stages, Decorations(), "p")
    with pytest.raises(AppValueError):
        base.stage("s", None)  # type: ignore[arg-type]
    with pytest.raises(AppValueError):
        base.stage("s", AsyncMock()).then(None)  # type: ignore[arg-type]


def _service() -> MagicMock:
    """Return a CrudService mock answering every CRUD call."""
    svc = MagicMock(spec=CrudService)
    svc.get = AsyncMock(return_value="e1")
    svc.list = AsyncMock(return_value=Page(items=["e1"], total=1))
    svc.create = AsyncMock(side_effect=lambda e: e)
    svc.update = AsyncMock(side_effect=lambda _id, e: e)
    svc.delete = AsyncMock(return_value=None)
    return svc


@pytest.mark.asyncio
async def test_crud_stages_every_role(observed: Observed) -> None:
    """A CRUD pipeline reaches the service under each role's operation name (Go ``Base.CRUD``).

    Why this test is important:
      - A CRUD resource's pipeline is assembled from the base over its service; a dropped or
        misrouted role would break every workflow above it.

    What it tests:
      - Each of the five roles returns the service's answer in a ``widgets.<op>`` span.
    """
    pipe = PipelineBase(
        CrudService,
        Decorations.fixed(Deps(observability=observed.observability)),
        "widgets",
    ).crud(_service())

    assert await pipe.get("1") == "e1"
    assert (await pipe.list(None, PageRequest())).total == 1
    assert await pipe.create("e") == "e"
    assert await pipe.update("1", "e") == "e"
    await pipe.delete("1")
    ops = ["get", "list", "create", "update", "delete"]
    assert [name for name, _, _ in observed.spans] == [f"widgets.{op}" for op in ops]
