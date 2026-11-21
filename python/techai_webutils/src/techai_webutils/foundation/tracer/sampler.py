"""Trace sampler kinds: ``ratio`` (the default) and ``link_aware``.

``link_aware`` is ``ratio`` that also samples a root span holding a link to a sampled
span, so a batch span (one that links to every item it carries) is never dropped while a
trace pointing at it is kept. Links are read when the span starts, so the caller passes
them to ``start_span`` rather than adding them afterwards.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from opentelemetry import trace
from opentelemetry.sdk.trace.sampling import (
    Decision,
    ParentBased,
    Sampler,
    SamplingResult,
    TraceIdRatioBased,
)

from techai_webutils.core.errors import InvalidInputError

if TYPE_CHECKING:
    from collections.abc import Sequence

    from opentelemetry.context import Context
    from opentelemetry.trace import Link, SpanKind, TraceState
    from opentelemetry.util.types import Attributes

SAMPLER_RATIO = "ratio"
"""Sample a root span by the configured rate and honour its parent's decision."""
SAMPLER_LINK_AWARE = "link_aware"
"""``ratio`` that also samples a root span linked to a sampled span."""


@final
class LinkAwareSampler(Sampler):
    """Root sampler that samples a span linked to a sampled span, else delegates.

    Wrap it in ``ParentBased`` (``build_sampler`` does) so it sees root spans only and
    a child still follows its parent.
    """

    def __init__(self, ratio: Sampler) -> None:
        """Store the sampler that decides every span with no sampled link.

        Args:
            ratio: The sampler for a span with no sampled link.

        """
        self._ratio = ratio

    @override
    def should_sample(
        self,
        parent_context: Context | None,
        trace_id: int,
        name: str,
        kind: SpanKind | None = None,
        attributes: Attributes = None,
        links: Sequence[Link] | None = None,
        trace_state: TraceState | None = None,
    ) -> SamplingResult:
        """Sample the span when any link points at a sampled span context.

        Args:
            parent_context: The parent context, None for a root span.
            trace_id: The span's trace id.
            name: The span name.
            kind: The span kind.
            attributes: The attributes the span starts with.
            links: The links the span starts with.
            trace_state: The trace state to propagate.

        Returns:
            ``RECORD_AND_SAMPLE`` keeping the parent's trace state when a link is
            sampled, else the ratio sampler's decision.

        """
        if any(link.context.trace_flags.sampled for link in links or ()):
            parent = trace.get_current_span(parent_context).get_span_context()
            return SamplingResult(
                Decision.RECORD_AND_SAMPLE, attributes, parent.trace_state
            )
        return self._ratio.should_sample(
            parent_context, trace_id, name, kind, attributes, links, trace_state
        )

    @override
    def get_description(self) -> str:
        """Name the sampler and the ratio sampler it falls back to.

        Returns:
            The description.

        """
        return f"LinkAware{{{self._ratio.get_description()}}}"


def build_sampler(kind: str, rate: float) -> Sampler:
    """Build the sampler ``kind`` selects for ``rate`` (0.0 to 1.0; 1.0 = always).

    Args:
        kind: ``ratio`` (or empty) or ``link_aware``.
        rate: Fraction of root traces to sample.

    Returns:
        The sampler, wrapped in ``ParentBased`` so a caller's decision is honoured.

    Raises:
        InvalidInputError: ``kind`` is neither ``ratio`` nor ``link_aware``; a misspelt
            kind fails the boot instead of sampling by another rule.

    """
    if kind in {"", SAMPLER_RATIO}:
        return ParentBased(root=TraceIdRatioBased(rate))
    if kind == SAMPLER_LINK_AWARE:
        return ParentBased(root=LinkAwareSampler(TraceIdRatioBased(rate)))
    msg = (
        f'unknown tracer sampler kind {kind!r}: want "{SAMPLER_RATIO}" or '
        f'"{SAMPLER_LINK_AWARE}"'
    )
    raise InvalidInputError(msg)
