"""Carries the trace across a message broker (mirrors Go ``foundation/tracer`` ``Inject`` / ``Extract``).

A publisher writes the current context (traceparent, tracestate, baggage) into a message's
string metadata with ``inject``; the consumer runs its handler inside ``continued(metadata)``, so
the handler's spans continue the publisher's trace.
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING

from opentelemetry import context as otel_context
from opentelemetry import propagate

if TYPE_CHECKING:
    from collections.abc import Generator, Mapping, MutableMapping


def inject(carrier: MutableMapping[str, str]) -> None:
    """Write the current propagated context into ``carrier`` (nothing when there is none)."""
    propagate.inject(carrier)


@contextmanager
def continued(carrier: Mapping[str, str] | None) -> Generator[None]:
    """Run the block in the context ``inject`` wrote into ``carrier``.

    An empty or absent carrier leaves the current context unchanged.
    """
    if not carrier:
        yield
        return
    token = otel_context.attach(propagate.extract(carrier))
    try:
        yield
    finally:
        otel_context.detach(token)
