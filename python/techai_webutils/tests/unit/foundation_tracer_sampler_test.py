"""Unit tests for the trace samplers.

The ``link_aware`` kind keeps a root span that links to a sampled span.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st
from opentelemetry.sdk.trace.sampling import Decision, Sampler
from opentelemetry.trace import (
    Link,
    NonRecordingSpan,
    SpanContext,
    SpanKind,
    TraceFlags,
    set_span_in_context,
)

from techai_webutils.core.errors import ErrorCode, InvalidInputError
from techai_webutils.foundation.tracer.sampler import LinkAwareSampler, build_sampler

if TYPE_CHECKING:
    from opentelemetry.context import Context

_TRACE_ID = 0x0123456789ABCDEF0123456789ABCDEF
_SPAN_ID = 0x0123456789ABCDEF
_NEVER = 0.0
_ALWAYS = 1.0


def _context(flags: int, *, remote: bool = True) -> SpanContext:
    """Return a span context with the given trace flags.

    Args:
        flags: The trace flags (``TraceFlags.SAMPLED`` or 0).
        remote: Whether the context came from another process.

    Returns:
        The span context.

    """
    return SpanContext(
        trace_id=_TRACE_ID,
        span_id=_SPAN_ID,
        is_remote=remote,
        trace_flags=TraceFlags(flags),
    )


def _link(flags: int) -> Link:
    """Return a link to a span context with the given trace flags.

    Args:
        flags: The trace flags of the linked span.

    Returns:
        The link.

    """
    return Link(_context(flags))


def _decide(
    sampler: Sampler,
    *links: Link,
    parent: Context | None = None,
) -> Decision:
    """Return the decision of ``sampler`` for a span started with ``links``.

    Args:
        sampler: The sampler under test.
        *links: The links the span starts with.
        parent: The parent context; None starts a root span.

    Returns:
        The sampling decision.

    """
    return sampler.should_sample(
        parent, _TRACE_ID, "span", kind=SpanKind.INTERNAL, links=links
    ).decision


def test_link_aware_samples_a_root_linked_to_a_sampled_span() -> None:
    """Test the sampler's one addition to the ratio sampler.

    **Why this test is important:**
      - A batch span (one KB round) links to every document it carries and is the root of
        its own trace. At a low ratio the round would be dropped while a sampled
        document's trace points at it, leaving a dangling link; a sampled linked span
        must keep the round.

    **What it tests:**
      - At a rate that drops every root, a root span with a link to a sampled span
        context is sampled.
      - A link to an unsampled span context does not force sampling.
    """
    sampler = build_sampler("link_aware", _NEVER)

    assert _decide(sampler, _link(TraceFlags.SAMPLED), _link(0)) == (
        Decision.RECORD_AND_SAMPLE
    )
    assert _decide(sampler, _link(0)) == Decision.DROP


def test_link_aware_unlinked_root_follows_the_ratio() -> None:
    """Test that a root with no links is sampled exactly as the ratio sampler decides.

    **Why this test is important:**
      - Link awareness must not raise trace volume for ordinary traffic.

    **What it tests:**
      - An unlinked root is dropped at rate 0 and sampled at rate 1.
    """
    assert _decide(build_sampler("link_aware", _NEVER)) == Decision.DROP
    assert _decide(build_sampler("link_aware", _ALWAYS)) == Decision.RECORD_AND_SAMPLE


def test_link_aware_honours_the_parent() -> None:
    """Test that a span with a parent follows the parent, as the ratio sampler does.

    **Why this test is important:**
      - A request that arrives with a sampled traceparent is force-sampled by callers who
        rely on it, and a child of an unsampled parent must stay out of the trace; the
        link rule applies to roots only, so those guarantees hold unchanged.

    **What it tests:**
      - At rate 0, a child of a sampled remote parent is sampled.
      - At rate 1, a child of an unsampled remote parent is dropped, even with a link to
        a sampled span.
    """
    sampled = set_span_in_context(NonRecordingSpan(_context(TraceFlags.SAMPLED)))
    unsampled = set_span_in_context(NonRecordingSpan(_context(0)))

    assert _decide(build_sampler("link_aware", _NEVER), parent=sampled) == (
        Decision.RECORD_AND_SAMPLE
    )
    assert (
        _decide(
            build_sampler("link_aware", _ALWAYS),
            _link(TraceFlags.SAMPLED),
            parent=unsampled,
        )
        == Decision.DROP
    )


@pytest.mark.parametrize("kind", ["", "ratio"])
def test_build_sampler_ratio_ignores_links(kind: str) -> None:
    """Test that the default kinds keep the ratio behaviour.

    **Why this test is important:**
      - The kind is config. A service that sets nothing must be unchanged.

    **What it tests:**
      - The empty kind and ``ratio`` drop a linked root at rate 0.

    Args:
        kind: The sampler kind under test.

    """
    assert _decide(build_sampler(kind, _NEVER), _link(TraceFlags.SAMPLED)) == (
        Decision.DROP
    )


def test_build_sampler_rejects_an_unknown_kind() -> None:
    """Test that a misspelt kind fails loudly instead of sampling by another rule.

    **Why this test is important:**
      - A silent fallback would sample by the wrong rule in production unnoticed.

    **What it tests:**
      - An unknown kind raises ``InvalidInputError`` carrying ``INVALID_INPUT``.
      - ``link_aware`` names itself in its description.
    """
    with pytest.raises(InvalidInputError) as raised:
        _ = build_sampler("link-aware", _NEVER)

    assert raised.value.code == ErrorCode.INVALID_INPUT
    assert (
        "LinkAware" in LinkAwareSampler(build_sampler("ratio", _NEVER)).get_description()
    )


@settings(max_examples=200, deadline=None)
@given(
    trace_id=st.integers(min_value=1, max_value=(1 << 128) - 1),
    rate=st.floats(min_value=0.0, max_value=1.0),
    sampled_flags=st.lists(st.booleans(), max_size=5),
)
def test_link_aware_is_ratio_plus_sampled_links(
    trace_id: int, rate: float, sampled_flags: list[bool]
) -> None:
    """Test the sampler against its specification over generated inputs.

    **Why this test is important:**
      - The contract is exact: every root the ratio sampler samples, plus every root with
        a sampled link, and nothing else. Example tests pin the corners; this pins the
        whole input space, so a change that raises trace volume or drops a linked batch
        span for some trace id or rate is caught.

    **What it tests:**
      - For random trace ids, rates in [0, 1] and link sets with random sampled flags, a
        root span is sampled by ``link_aware`` exactly when the ratio sampler samples it
        or any link is sampled.

    Args:
        trace_id: The trace id of the root span, as an integer.
        rate: The ratio sampler's rate.
        sampled_flags: One entry per link: whether the linked span context is sampled.

    """
    links = tuple(
        _link(TraceFlags.SAMPLED if sampled else TraceFlags.DEFAULT)
        for sampled in sampled_flags
    )

    def decide(kind: str) -> Decision:
        return (
            build_sampler(kind, rate)
            .should_sample(None, trace_id, "span", kind=SpanKind.INTERNAL, links=links)
            .decision
        )

    ratio_samples = decide("ratio") == Decision.RECORD_AND_SAMPLE
    link_aware_samples = decide("link_aware") == Decision.RECORD_AND_SAMPLE

    assert link_aware_samples == (ratio_samples or any(sampled_flags))
