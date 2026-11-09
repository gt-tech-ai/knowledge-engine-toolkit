"""NLP annotators (``AnnotatorKind``), their factory, the ruler and the ``pii`` wrappers.

The kinds are ``noop``, ``spacy``, ``gliner`` and ``grpc``; ``EntityRulerAnnotator`` adds
an org's phrase patterns and ``PiiAnnotator`` answers the in-process ``pii`` task.
"""

from techai_webutils.clients.nlp.builder import (
    AnnotatorConfig,
    AnnotatorKind,
    OnUnavailable,
    annotator_from_config,
)
from techai_webutils.clients.nlp.degrading import DegradingAnnotator
from techai_webutils.clients.nlp.pii import PII, PiiAnnotator, PiiKind, PiiMode, PiiSpan
from techai_webutils.clients.nlp.ruler import EntityRulerAnnotator, RulerPattern

__all__ = [
    "PII",
    "AnnotatorConfig",
    "AnnotatorKind",
    "DegradingAnnotator",
    "EntityRulerAnnotator",
    "OnUnavailable",
    "PiiAnnotator",
    "PiiKind",
    "PiiMode",
    "PiiSpan",
    "RulerPattern",
    "annotator_from_config",
]
