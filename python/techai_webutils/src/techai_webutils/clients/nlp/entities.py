"""Shared checks of the entity-only annotator backends.

They are ``noop``, ``spacy`` and ``gliner``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import ENTITIES

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.annotator import TaskRegistry


def require_entities_only(
    registry: TaskRegistry, tasks: frozenset[str], backend: str
) -> None:
    """Raise ``INVALID_INPUT`` for an unregistered task, or one ``backend`` cannot run.

    These backends only find entities; a consumer's own task needs a backend that runs it
    (the ``grpc`` kind, behind the consumer's NLP service).

    Args:
        registry: Says which tasks exist and which backend runs each.
        tasks: The requested tasks.
        backend: The backend's name.

    Raises:
        AppValueError: The ``backend`` annotator only finds entities; it cannot run
            ``other``.

    """
    registry.require(tasks)
    other = sorted(tasks - {ENTITIES})
    if other:
        msg = f"the {backend} annotator only finds entities; it cannot run {other}"
        raise AppValueError(msg)
