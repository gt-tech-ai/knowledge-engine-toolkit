"""Eval graders: a config-selected ``Grader``.

The kinds are ``stub``, ``parse`` (parse quality) and ``ranking``.

``ranking`` scores retrieval: recall@k, nDCG@k and reciprocal rank against relevance
judgments.
"""

from techai_webutils.clients.grader.builder import (
    GraderConfig,
    GraderKind,
    grader_from_config,
)

__all__ = ["GraderConfig", "GraderKind", "grader_from_config"]
