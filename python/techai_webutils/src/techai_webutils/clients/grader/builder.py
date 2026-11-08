"""Config-selected ``Grader`` factory (the ``clients/audit/builder.py`` pattern).

``GraderConfig.kind`` selects ``stub`` (the default: always passes), ``parse`` (parse quality
against golden blocks, one ``ParseAspect`` per grader) or ``ranking`` (a retrieval ranking against
relevance judgments, one ``RankingMetric`` at cutoff ``k`` per grader). An unknown kind, a
``parse`` grader with no or an unknown aspect, a ``ranking`` grader with an unknown metric or a
cutoff below 1, or a pass score outside ``[0, 1]`` raises ``AppError(INVALID_INPUT)``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.grader.parse import ParseAspect, ParseGrader
from techai_webutils.clients.grader.ranking import RankingGrader, RankingMetric
from techai_webutils.clients.grader.stub import StubGrader
from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.grader import Grader


class GraderKind(StrEnum):
    """The built-in grader backends."""

    STUB = "stub"
    """Always passes with a perfect score (zero infrastructure)."""
    PARSE = "parse"
    """Scores one aspect of a parse against the case's golden blocks."""
    RANKING = "ranking"
    """Scores one ranking metric at a cutoff against the case's relevance judgments."""


@dataclass(frozen=True, slots=True)
class GraderConfig:
    """Grader configuration."""

    kind: str = GraderKind.STUB
    """A ``GraderKind`` value."""
    aspect: str = ""
    """The ``ParseAspect`` a ``parse`` grader scores; unused by other kinds."""
    metric: str = ""
    """The ``RankingMetric`` a ``ranking`` grader scores; unused by other kinds."""
    k: int = 10
    """The cutoff a ``ranking`` grader scores at, at least 1; unused by other kinds."""
    pass_score: float = 0.9
    """The score in ``[0, 1]`` a graded output must reach to pass."""

    def __post_init__(self) -> None:
        """Reject a pass score outside ``[0, 1]`` (``INVALID_INPUT``)."""
        if not 0.0 <= self.pass_score <= 1.0:
            msg = f"grader pass_score {self.pass_score} is outside [0, 1]"
            raise AppError(ErrorCode.INVALID_INPUT, msg)


def grader_from_config(config: GraderConfig) -> Grader:
    """Build the ``Grader`` selected by ``config.kind``.

    Args:
        config: The grader configuration.

    Returns:
        The grader.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown kind, a ``parse`` kind with no or an
            unknown aspect, or a ``ranking`` kind with an unknown metric or a cutoff below 1.

    """
    if config.kind == GraderKind.STUB:
        return StubGrader()
    if config.kind == GraderKind.RANKING:
        return _ranking(config)
    if config.kind != GraderKind.PARSE:
        raise AppError(ErrorCode.INVALID_INPUT, f"unknown grader kind: {config.kind!r}")
    if config.aspect not in set(ParseAspect):
        msg = f"unknown parse grader aspect: {config.aspect!r}"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return ParseGrader(ParseAspect(config.aspect), config.pass_score)


def _ranking(config: GraderConfig) -> Grader:
    """Build the ``ranking`` grader ``config`` names.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown metric or a cutoff below 1.

    """
    if config.metric not in set(RankingMetric):
        msg = f"unknown ranking grader metric: {config.metric!r}"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    if config.k < 1:
        msg = f"ranking grader k must be at least 1, got {config.k}"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return RankingGrader(RankingMetric(config.metric), config.k, config.pass_score)
