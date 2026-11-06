"""Eval graders: a config-selected ``Grader`` (``stub``, and ``parse`` for parse quality)."""

from techai_webutils.clients.grader.builder import (
    GraderConfig,
    GraderKind,
    grader_from_config,
)

__all__ = ["GraderConfig", "GraderKind", "grader_from_config"]
