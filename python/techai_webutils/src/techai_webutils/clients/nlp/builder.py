"""``annotator_from_config``: the config-selected ``Annotator``.

``noop`` (the default) annotates nothing; ``spacy`` runs a spaCy pipeline's ``ner`` in
process (the ``nlp`` extra, imported only when selected); ``gliner`` runs an injected
GLiNER model; ``grpc`` calls the consumer's NLP service through an injected
``AnnotateClient``. With ``on_unavailable = degrade`` the annotator is wrapped in
``DegradingAnnotator``.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.nlp.degrading import DegradingAnnotator
from techai_webutils.clients.nlp.gliner.annotator import GlinerAnnotator
from techai_webutils.clients.nlp.grpc.annotator import GrpcAnnotator
from techai_webutils.clients.nlp.noop.annotator import NoopAnnotator
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import TaskRegistry

if TYPE_CHECKING:
    from techai_webutils.clients.nlp.gliner.annotator import GlinerModel
    from techai_webutils.clients.nlp.spacy.annotator import SpacyAnnotator
    from techai_webutils.core.interfaces.annotator import AnnotateClient, Annotator
    from techai_webutils.core.interfaces.metrics import MetricsProvider


class AnnotatorKind(StrEnum):
    """Which annotator to build."""

    NOOP = "noop"
    """Annotates nothing (the default)."""
    SPACY = "spacy"
    """A spaCy pipeline's ``ner``, in process."""
    GLINER = "gliner"
    """An injected GLiNER model, for the configured labels."""
    GRPC = "grpc"
    """The consumer's NLP service, through an injected ``AnnotateClient``."""


class OnUnavailable(StrEnum):
    """What a transient backend failure does."""

    WAIT = "wait"
    """Raise it, so the caller retries (the default)."""
    DEGRADE = "degrade"
    """Return empty, degraded annotations and count them (``DegradingAnnotator``)."""


@dataclass(frozen=True, slots=True)
class AnnotatorConfig:
    """Selects the annotator and its model."""

    kind: AnnotatorKind = AnnotatorKind.NOOP
    """The annotator to build; ``noop`` by default."""
    model: str = ""
    """``spacy``: the pipeline package or path (required); ``gliner``: the model name
    reported on each annotation."""
    labels: tuple[str, ...] = ()
    """``gliner``: the labels to find (required); ``spacy``: the labels kept (empty keeps
    all)."""
    threshold: float = 0.5
    """``gliner``: the lowest score kept."""
    batch_size: int = 64
    """``spacy``: texts per ``nlp.pipe`` batch."""
    on_unavailable: OnUnavailable = OnUnavailable.WAIT
    """``wait`` raises a transient failure; ``degrade`` returns degraded annotations."""


def annotator_from_config(
    config: AnnotatorConfig,
    *,
    registry: TaskRegistry | None = None,
    gliner_model: GlinerModel | None = None,
    client: AnnotateClient | None = None,
    metrics: MetricsProvider | None = None,
) -> Annotator:
    """Build the ``Annotator`` ``config.kind`` selects.

    ``registry`` holds the tasks callers may ask for (default: ``ENTITIES`` only);
    ``gliner_model`` and ``client`` are the ``gliner`` and ``grpc`` kinds' injected
    backends; ``metrics`` counts degraded blocks.

    Returns:
        The ``Annotator`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind or ``on_unavailable``, or a kind missing what it
            needs (``INVALID_INPUT``).

    """
    tasks = registry or TaskRegistry()
    annotator = _backend(config, tasks, gliner_model, client)
    if config.on_unavailable == OnUnavailable.WAIT:
        return annotator
    if config.on_unavailable == OnUnavailable.DEGRADE:
        return DegradingAnnotator(annotator, metrics=metrics)
    msg = f"unknown on_unavailable: {config.on_unavailable!r}"
    raise AppValueError(msg)


def _backend(
    config: AnnotatorConfig,
    registry: TaskRegistry,
    gliner_model: GlinerModel | None,
    client: AnnotateClient | None,
) -> Annotator:
    """Build the bare backend ``config.kind`` selects.

    Returns:
        The bare backend ``config.kind`` selects.

    Raises:
        AppValueError: The gliner annotator needs an injected model and at least one
            label; the grpc annotator needs an annotate client; unknown annotator kind:
            ``config.kind``.

    """
    if config.kind == AnnotatorKind.NOOP:
        return NoopAnnotator(registry)
    if config.kind == AnnotatorKind.SPACY:
        return _spacy(config, registry)
    if config.kind == AnnotatorKind.GLINER:
        if gliner_model is None or not config.labels:
            msg = "the gliner annotator needs an injected model and at least one label"
            raise AppValueError(msg)
        return GlinerAnnotator(
            gliner_model,
            name=config.model or "gliner",
            labels=config.labels,
            threshold=config.threshold,
            registry=registry,
        )
    if config.kind == AnnotatorKind.GRPC:
        if client is None:
            msg = "the grpc annotator needs an annotate client"
            raise AppValueError(msg)
        return GrpcAnnotator(client, registry=registry)
    msg = f"unknown annotator kind: {config.kind!r}"
    raise AppValueError(msg)


def _spacy(config: AnnotatorConfig, registry: TaskRegistry) -> Annotator:
    """Build the spacy annotator; spaCy is imported only on this path.

    Returns:
        The spacy annotator; spaCy is imported only on this path.

    Raises:
        AppValueError: The spacy annotator needs a pipeline (AnnotatorConfig.model).

    """
    if not config.model:
        msg = "the spacy annotator needs a pipeline (AnnotatorConfig.model)"
        raise AppValueError(msg)
    backend = importlib.import_module("techai_webutils.clients.nlp.spacy.annotator")
    spacy_annotator = cast("type[SpacyAnnotator]", backend.SpacyAnnotator)
    return spacy_annotator(
        config.model,
        labels=config.labels,
        batch_size=config.batch_size,
        registry=registry,
    )
