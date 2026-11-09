"""``ResumePlanner``: the first stage a document still has to run.

The golden cases in ``testdata/resume_planner/`` are the shared contract a port of this
planner (e.g. vertiva_ekp's Go service) replays.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final

from techai_webutils.core.interfaces.checkpoint import Stage

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.checkpoint import StageState

SINK_BEDROCK = "bedrock"
"""The sink that embeds by itself, so ``Stage.EMBED`` is never run for it."""


_DEFAULT_EMBEDDING_SINKS: frozenset[str] = frozenset({SINK_BEDROCK})
"""The default ``embedding_sinks``."""


@final
class ResumePlanner:
    """Plans where a document's pipeline resumes from its stage records."""

    def __init__(
        self, embedding_sinks: frozenset[str] = _DEFAULT_EMBEDDING_SINKS
    ) -> None:
        """Plan for a pipeline where ``embedding_sinks`` embed by themselves.

        (no ``EMBED``).
        """
        self._embedding_sinks = embedding_sinks

    def plan(
        self, states: Sequence[StageState], current: Mapping[Stage, str], *, sink: str
    ) -> Stage | None:
        """Return the first stage whose record is missing or stale.

        Or ``None`` when indexed.

        A stage is done when a record carries the fingerprint ``current`` gives it. Stages
        run in order, so the first stage not done is where the pipeline resumes: every
        later
            stage runs
        again even if it has a record, since its upstream is about to change. ``current``
        holds each stage's fingerprint for today's versions; a stage it omits is never
        done. For a sink in ``embedding_sinks`` (Bedrock by default) ``Stage.EMBED`` is
        skipped.

        Returns:
            The first stage whose record is missing or stale.

        """
        done = {(state.stage, state.fingerprint) for state in states}
        for stage in Stage:
            if stage is Stage.EMBED and sink in self._embedding_sinks:
                continue
            want = current.get(stage)
            if want is None or (stage, want) not in done:
                return stage
        return None
