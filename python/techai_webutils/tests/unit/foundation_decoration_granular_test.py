"""Tests for per-component decoration (mirrors Go ``foundation_decoration_granular_test.go``).

The override cascade, the layer check, ``verify`` and the per-name pipeline, job and gate bases.
"""

from __future__ import annotations

import dataclasses
from typing import Protocol
from unittest.mock import MagicMock

import pytest

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.execution import BatchResult, JobMeta
from techai_webutils.execution.gate import new_gate, run_gate
from techai_webutils.execution.gate import tier as gate_tier
from techai_webutils.execution.job.base import JobBase
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import (
    Collaborators,
    Decorations,
    Observability,
)
from techai_webutils.pipelines.pipeline import PipelineBase
from techai_webutils.pipelines.pipeline import tier as pipeline_tier
from techai_webutils.repos.repository import tier as repository_tier
from techai_webutils.workflows.workflow import tier as workflow_tier


def _with(
    config: decoration.DecorationConfig, **tiers: dict[str, decoration.Layers]
) -> decoration.DecorationConfig:
    """Return ``config`` with each named tier's overrides replaced."""
    return dataclasses.replace(
        config,
        **{
            name: dataclasses.replace(getattr(config, name), overrides=overrides)
            for name, overrides in tiers.items()
        },
    )


def test_layers_for_cascades_overrides_along_the_names_dots() -> None:
    """An override applies to its name and every name beneath it, the deepest winning.

    Why this test is important:
      - One key must cover an app or a resource and a deeper key one component, or the YAML
        either repeats itself or cannot reach a single stage.

    What it tests:
      - ``api.documents.create`` gets the defaults, ``api``, ``api.documents``, then its own.
      - A sibling (``api.users.create``) gets only the defaults and ``api``.
    """
    tier = decoration.Tier(
        defaults=decoration.Layers(logging=False, tracing=False),
        overrides={
            "api": decoration.Layers(logging=True, timeout=5.0),
            "api.documents": decoration.Layers(tracing=True),
            "api.documents.create": decoration.Layers(timeout=2.0),
        },
    )

    create = tier.layers_for("api.documents.create")
    assert decoration.on(create.logging)
    assert decoration.on(create.tracing)
    assert create.timeout == pytest.approx(2.0)
    sibling = tier.layers_for("api.users.create")
    assert decoration.on(sibling.logging)
    assert not decoration.on(sibling.tracing)
    assert sibling.timeout == pytest.approx(5.0)


def test_deps_refuses_an_override_turning_on_a_layer_its_tier_lacks() -> None:
    """An override that enables a layer its tier does not run fails loudly.

    Why this test is important:
      - Retry on a workflow would be accepted and do nothing, so the operator believes a
        protection is in place that is not.

    What it tests:
      - Enabling retry on a workflow raises ``AppValueError`` naming the component and layer.
      - Turning it off resolves, and a breaker on a repository (which runs one) resolves.
    """
    config = _with(
        decoration.default_config(),
        workflows={
            "app.ask": decoration.Layers(retry=decoration.Retry(enabled=True)),
            "app.quiet": decoration.Layers(retry=decoration.Retry(enabled=False)),
        },
    )
    decorations = Decorations(config)

    with pytest.raises(AppValueError, match=r"app\.ask turns on retry"):
        decorations.deps(workflow_tier(), "app.ask")
    decorations.deps(workflow_tier(), "app.quiet")
    assert decorations.deps(repository_tier(), "app.docs").breaker is not None


def test_verify_fails_on_an_override_no_component_uses() -> None:
    """``verify`` names every override under its prefixes that configures nothing.

    Why this test is important:
      - A misspelled or stale key configures nothing and says nothing; and several
        deployables share one config, so each may judge only its own keys.

    What it tests:
      - An unused ``app.documnets`` repository override fails; ``other.users`` is ignored;
        the ``app`` pipeline override counts as used by ``app.documents.create``.
      - Without the stray key ``verify`` passes; a ``subject`` resolution error is reported.
    """
    on = decoration.Layers(tracing=True)
    config = _with(
        decoration.default_config(),
        repositories={"app.documents": on, "app.documnets": on, "other.users": on},
        pipelines={"app": decoration.Layers(logging=True)},
    )
    decorations = Decorations(config)
    decorations.deps(repository_tier(), "app.documents")
    decorations.subject(pipeline_tier(), "app.documents.create")

    with pytest.raises(AppValueError) as raised:
        decorations.verify("app")
    assert "repositories.overrides.app.documnets" in str(raised.value)
    assert "other.users" not in str(raised.value)
    assert "pipelines.overrides.app," not in str(raised.value)

    clean_config = _with(config, repositories={"app.documents": on, "other.users": on})
    clean = Decorations(clean_config)
    clean.deps(repository_tier(), "app.documents")
    clean.subject(pipeline_tier(), "app.documents.create")
    clean.verify("app")

    broken = Decorations(
        _with(
            clean_config,
            workflows={
                "app.ask": decoration.Layers(retry=decoration.Retry(enabled=True))
            },
        )
    )
    broken.subject(workflow_tier(), "app.ask")
    with pytest.raises(AppValueError, match="retry"):
        broken.verify("app")


class _Documents(Protocol):
    """A pipeline contract with two stages, create and delete."""

    async def create(self, value: str) -> str:
        """Run the create stage."""
        ...

    async def delete(self, value: str) -> str:
        """Run the delete stage."""
        ...


@pytest.mark.asyncio
async def test_configured_bases_resolve_each_stage_job_and_gate_by_its_own_name() -> None:
    """A stage, a job and a gate each take their own decoration under a qualified name.

    Why this test is important:
      - Resolving a pipeline set or a job group under one shared name makes every stage or
        job share one decoration, so none can be tuned on its own.

    What it tests:
      - The ``create`` stage of a pipeline built from its contract as ``app.documents`` is
        logged as ``app.documents.create`` when that override turns logging on; its sibling
        ``delete`` is not.
      - A job built as ``events.sweep`` runs as its ``execute`` operation; a gate built with
        ``decorate`` runs as ``run`` under its own name in the gate tier; ``verify`` passes.
    """
    logger = MagicMock()
    logged: list[str] = []
    logger.debug.side_effect = lambda _msg, **fields: logged.extend(  # pyright: ignore[reportUnknownLambdaType]
        f"{v}.{fields['operation']}" for k, v in fields.items() if k != "operation"
    )
    config = _with(
        decoration.default_config(),
        pipelines={"app.documents.create": decoration.Layers(logging=True)},
        jobs={"events.sweep": decoration.Layers(logging=True)},
        gates={"events.nightly": decoration.Layers(logging=True)},
    )
    decorations = Decorations(
        config, Collaborators(observability=Observability(logger=logger))
    )

    async def identity(value: str) -> str:  # noqa: RUF029 - a stage is an async function
        return value

    pipes = PipelineBase(_Documents, decorations, "app.documents")
    assert await pipes.stage("create", identity).execute("x") == "x"
    assert await pipes.stage("delete", identity).execute("x") == "x"

    async def sweep() -> BatchResult:  # noqa: RUF029 - a job's run is an async function
        return BatchResult()

    job = JobBase(decorations, "events.sweep").func(
        JobMeta(name="sweep", group="events"), sweep
    )
    gate = new_gate("events.nightly").serial("sweep", job).decorate(decorations).build()
    await run_gate(gate)

    assert "app.documents.create" in logged
    assert "app.documents.delete" not in logged
    assert "events.sweep.execute" in logged
    assert "events.nightly.run" in logged
    decorations.verify("app", "events")
    assert gate_tier().name == "gate"
