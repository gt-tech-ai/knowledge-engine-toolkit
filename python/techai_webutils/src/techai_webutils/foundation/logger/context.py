"""Correlation fields every log line inside a block carries, without hand-formatting them.

A consumer handling one message (a stage job, a relay, a reaper) binds the message's ids
once with ``log_context``; every line logged while the block runs — by the consumer, the
handler it calls, or a decorator around a client — carries them, beside the ``trace_id``
and ``span_id`` the logger adds from the active span. The fields live in context
variables, so concurrent asyncio tasks each keep their own and a field never leaks past
the block.

The structlog Logger kind (the default) merges them; the stdlib kind does not.
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING

import structlog

if TYPE_CHECKING:
    from collections.abc import Generator


@contextmanager
def log_context(**fields: object) -> Generator[None]:
    """Bind ``fields`` to every log line logged inside the block.

    A field bound by an enclosing block is overridden inside and restored on exit.

    Args:
        **fields: The correlation fields (snake_case keys, ids and small values only).

    Yields:
        Nothing; the fields apply while the block runs.

    """
    with structlog.contextvars.bound_contextvars(**fields):
        yield
