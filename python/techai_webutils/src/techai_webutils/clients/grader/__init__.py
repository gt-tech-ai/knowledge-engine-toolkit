"""Eval graders: a config-selected ``Grader`` (``stub``, ``parse`` for parse quality, ``ranking``).

``ranking`` scores retrieval: recall@k, nDCG@k and reciprocal rank against relevance judgments.
"""

from techai_webutils.clients.grader.builder import (
    GraderConfig,
    GraderKind,
    grader_from_config,
)

__all__ = ["GraderConfig", "GraderKind", "grader_from_config"]
