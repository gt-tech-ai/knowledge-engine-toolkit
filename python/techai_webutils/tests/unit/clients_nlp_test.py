"""Unit tests for the annotator Kinds: noop, spacy (real spaCy, no download), gliner and grpc."""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import grpc
import pytest
import spacy
from grpc.aio import AioRpcError, Metadata
from spacy.language import Language

from techai_webutils.clients.nlp import (
    AnnotatorConfig,
    AnnotatorKind,
    annotator_from_config,
)
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    AnnotateClient,
    Annotation,
    Entity,
    TextBlock,
)

if TYPE_CHECKING:
    from pathlib import Path

    from spacy.pipeline import EntityRuler
    from spacy.tokens import Doc

    from techai_webutils.clients.nlp.gliner.annotator import GlinerModel

_ENTITIES = frozenset({ENTITIES})
_BLOCKS = [TextBlock("Acme opened in Berlin.", "c1"), TextBlock("Nothing here.", "c2")]


@Language.component("explode_if_run")
def _explode_if_run(doc: Doc) -> Doc:
    """Fail when run: proves the annotator disabled every component but ``ner``."""
    msg = f"a disabled component ran on {doc.text!r}"
    raise AssertionError(msg)


@pytest.fixture
def pipeline(tmp_path: Path) -> str:
    """Save a small pipeline (a pattern ``ner`` and a component that must not run); return its path."""
    nlp = spacy.blank("en")
    ruler = cast("EntityRuler", nlp.add_pipe("entity_ruler", name="ner"))
    ruler.add_patterns([
        {"label": "ORG", "pattern": "Acme"},
        {"label": "GPE", "pattern": "Berlin"},
    ])
    # Added after the patterns: the ruler runs the earlier components on each phrase pattern.
    nlp.add_pipe("explode_if_run")
    nlp.to_disk(tmp_path / "pipeline")
    return str(tmp_path / "pipeline")


@pytest.mark.asyncio
async def test_spacy_kind_runs_only_ner_and_returns_spans_within_the_text(
    pipeline: str,
) -> None:
    """Test that the ``spacy`` kind loads the pipeline with only ``ner`` and returns valid spans.

    **Why this test is important:**
      - The tagger, parser and lemmatizer are most of a pipeline's CPU; entities are all the
        enrich stage needs, so every other component must stay off.

    **What it tests:**
      - a pipeline whose other component fails when run annotates without failing
      - the first block's entities are ``ORG`` "Acme" (0-4) and ``GPE`` "Berlin" (15-21), each
        span matching the text; the second block has none
      - with ``labels=("ORG",)`` only the ``ORG`` entity is kept
    """
    config = AnnotatorConfig(kind=AnnotatorKind.SPACY, model=pipeline)

    annotations = await annotator_from_config(config).annotate_batch(
        _BLOCKS, _ENTITIES, ""
    )
    orgs = await annotator_from_config(
        AnnotatorConfig(kind=AnnotatorKind.SPACY, model=pipeline, labels=("ORG",))
    ).annotate_batch(_BLOCKS, _ENTITIES, "")

    assert [(e.label, e.start, e.end, e.text) for e in annotations[0].entities] == [
        ("ORG", 0, 4, "Acme"),
        ("GPE", 15, 21, "Berlin"),
    ]
    assert annotations[1].entities == ()
    assert annotations[0].model == pipeline
    assert [e.label for e in orgs[0].entities] == ["ORG"]


@pytest.mark.asyncio
async def test_gliner_kind_returns_spans_for_the_configured_labels() -> None:
    """Test that the ``gliner`` kind asks the injected model for the configured labels.

    **Why this test is important:**
      - GLiNER finds labels a fixed model lacks (products, regulations); the configured labels
        and threshold must reach it, and its predictions must become checked entities.

    **What it tests:**
      - the model is called per block with the labels and the threshold
      - its predictions (out of order) become entities in text order with their scores
    """
    model = MagicMock()
    model.predict_entities = MagicMock(
        side_effect=[
            [
                {"start": 15, "end": 21, "text": "Berlin", "label": "City", "score": 0.7},
                {"start": 0, "end": 4, "text": "Acme", "label": "Company", "score": 0.9},
            ],
            [],
        ]
    )
    annotator = annotator_from_config(
        AnnotatorConfig(
            kind=AnnotatorKind.GLINER,
            model="gliner-small",
            labels=("Company", "City"),
            threshold=0.6,
        ),
        gliner_model=cast("GlinerModel", model),
    )

    annotations = await annotator.annotate_batch(_BLOCKS, _ENTITIES, "")

    model.predict_entities.assert_any_call(
        "Acme opened in Berlin.", ["Company", "City"], threshold=0.6
    )
    assert annotations[0].entities == (
        Entity("Company", 0, 4, "Acme", 0.9),
        Entity("City", 15, 21, "Berlin", 0.7),
    )
    assert (annotations[0].model, annotations[1].entities) == ("gliner-small", ())


@pytest.mark.asyncio
async def test_noop_kind_returns_no_entities_and_refuses_unknown_tasks() -> None:
    """Test that the default ``noop`` kind annotates nothing and still refuses unknown tasks.

    **Why this test is important:**
      - ``noop`` is the zero-infra default; a typo in a task name must fail there too, not
        only once a real backend is configured.

    **What it tests:**
      - ``AnnotatorConfig()`` builds a noop annotator returning one empty annotation per block
      - an unregistered task raises ``INVALID_INPUT``
    """
    annotator = annotator_from_config(AnnotatorConfig())

    annotations = await annotator.annotate_batch(_BLOCKS, _ENTITIES, "")

    assert [a.entities for a in annotations] == [(), ()]
    with pytest.raises(AppValueError):
        await annotator.annotate_batch(_BLOCKS, frozenset({"speaker"}), "")


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("status", "code"),
    [
        (grpc.StatusCode.UNAVAILABLE, ErrorCode.UNAVAILABLE),
        (grpc.StatusCode.DEADLINE_EXCEEDED, ErrorCode.TIMEOUT),
        (grpc.StatusCode.INVALID_ARGUMENT, ErrorCode.INTERNAL),
    ],
)
async def test_grpc_kind_maps_status_to_coded_errors(
    status: grpc.StatusCode, code: ErrorCode
) -> None:
    """Test that a failing NLP service raises a coded error, transient for UNAVAILABLE.

    **Why this test is important:**
      - The retry and degrading decorators act only on transient codes; a raw gRPC error would
        bypass both and fail the document.

    **What it tests:**
      - UNAVAILABLE → ``UNAVAILABLE`` and DEADLINE_EXCEEDED → ``TIMEOUT`` (both transient);
        INVALID_ARGUMENT → ``INTERNAL`` (permanent)
    """
    client = MagicMock()
    client.annotate_batch = AsyncMock(
        side_effect=AioRpcError(status, Metadata(), Metadata(), "nlp down")
    )
    annotator = annotator_from_config(
        AnnotatorConfig(kind=AnnotatorKind.GRPC), client=cast("AnnotateClient", client)
    )

    with pytest.raises(AppError) as raised:
        await annotator.annotate_batch(_BLOCKS, _ENTITIES, "interview")

    assert raised.value.code is code
    assert raised.value.is_transient is (code is not ErrorCode.INTERNAL)


@pytest.mark.asyncio
async def test_grpc_kind_returns_the_services_checked_annotations() -> None:
    """Test that the ``grpc`` kind passes the tasks and profile and returns the checked reply.

    **Why this test is important:**
      - The profile selects the service's task set (an ``interview`` profile); the reply must
        reach the caller unchanged once it matches the blocks.

    **What it tests:**
      - the client gets the blocks, the tasks and the profile; its reply is returned
    """
    reply = [Annotation(entities=(Entity("ORG", 0, 4, "Acme"),)), Annotation()]
    client = MagicMock()
    client.annotate_batch = AsyncMock(return_value=reply)
    annotator = annotator_from_config(
        AnnotatorConfig(kind=AnnotatorKind.GRPC), client=cast("AnnotateClient", client)
    )

    annotations = await annotator.annotate_batch(_BLOCKS, _ENTITIES, "interview")

    client.annotate_batch.assert_awaited_once_with(_BLOCKS, _ENTITIES, "interview")
    assert annotations == reply


def test_factory_refuses_what_a_kind_needs() -> None:
    """Test that each kind missing what it needs, and an unknown kind, fail at build.

    **Why this test is important:**
      - A misconfigured annotator must stop the deployable at start-up, not on the first
        document.

    **What it tests:**
      - ``spacy`` without a pipeline, ``gliner`` without a model or labels, ``grpc`` without a
        client, an unknown kind and an unknown ``on_unavailable`` raise ``AppValueError``
    """
    model = cast("GlinerModel", MagicMock())
    for config in (
        AnnotatorConfig(kind=AnnotatorKind.SPACY),
        AnnotatorConfig(kind=AnnotatorKind.GLINER, labels=("X",)),
        AnnotatorConfig(kind=AnnotatorKind.GRPC),
        AnnotatorConfig(kind=cast("AnnotatorKind", "bogus")),
    ):
        with pytest.raises(AppValueError):
            annotator_from_config(config)
    with pytest.raises(AppValueError):
        annotator_from_config(
            AnnotatorConfig(kind=AnnotatorKind.GLINER), gliner_model=model
        )
