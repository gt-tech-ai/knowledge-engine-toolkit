"""NLP annotators (``AnnotatorKind``: ``noop``, ``spacy``, ``gliner``, ``grpc``) and their factory."""

from techai_webutils.clients.nlp.builder import (
    AnnotatorConfig,
    AnnotatorKind,
    OnUnavailable,
    annotator_from_config,
)
from techai_webutils.clients.nlp.degrading import DegradingAnnotator

__all__ = [
    "AnnotatorConfig",
    "AnnotatorKind",
    "DegradingAnnotator",
    "OnUnavailable",
    "annotator_from_config",
]
