"""How isolated parse children start: the preloaded forkserver, or spawn.

Every isolated parse runs in its own child. Where the platform has ``forkserver``, the
child is forked from a server process that has already imported the heavy parser
libraries, so a parse pays a fork instead of an interpreter start and a re-import.
Platforms without it (e.g. Windows) fall back to ``spawn``.

CPython starts every forkserver or spawn child by re-importing the parent's main module
as ``__mp_main__``. When the parent is a service's console script, that re-runs the
service's whole import graph in every child before it does any work. Naming the main
module in the preload (``configure_forkserver_preload``) makes the forkserver import it
once, and each child then inherits it. ``spawn`` children have no server to inherit from
and always pay that import.

The preload and the server are process-global: ``configure_forkserver_preload`` must run
before the first child starts, and ``warm_forkserver`` starts the server early so the
first parse does not pay for the preload imports.
"""

from __future__ import annotations

import multiprocessing as mp
from multiprocessing import forkserver
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Sequence
    from multiprocessing.context import ForkServerContext, SpawnContext

DEFAULT_PRELOAD: tuple[str, ...] = (
    "markitdown",
    "pymupdf",
    "techai_webutils.clients.parsing.isolated.isolated",
)
"""Modules the forkserver always preloads: the parser libraries and the module that
holds the parse entries, so a child unpickles its target without importing anything."""


def _isolated_context() -> SpawnContext | ForkServerContext:
    """Return the context isolated parses start under: forkserver, else spawn.

    Setting the preload is configuration only; the forkserver imports it when it starts,
    so the parent stays free of the parser libraries.

    Returns:
        The forkserver context with the default preload, or spawn where forkserver is
        unsupported.

    """
    if "forkserver" in mp.get_all_start_methods():
        ctx = mp.get_context("forkserver")
        ctx.set_forkserver_preload(list(DEFAULT_PRELOAD))
        return ctx
    return mp.get_context("spawn")


ISOLATED_CONTEXT = _isolated_context()
"""The context isolated parses start under (forkserver where available, else spawn)."""

SPAWN_CONTEXT = mp.get_context("spawn")
"""A fresh-interpreter context, for parsers whose native libraries are not fork-safe
(Docling). Each child re-imports the parent's main module; nothing is preloaded."""


def configure_forkserver_preload(modules: Sequence[str]) -> None:
    """Preload ``modules`` in the forkserver, after the default preload.

    A service passes its own main module (e.g. ``myservice.__main__``) so children stop
    re-importing it as ``__mp_main__``. Duplicates are dropped and the order kept. Under
    ``spawn`` this does nothing.

    The preload is process-global and is read only when the forkserver starts: call this
    from the composition root before any parse or ``warm_forkserver``. A module that
    fails to import in the forkserver is skipped by CPython, so a wrong name costs the
    re-import, never the parse.

    Args:
        modules: Importable module names to preload, beyond the defaults.

    """
    if ISOLATED_CONTEXT.get_start_method() != "forkserver":
        return
    preload = list(dict.fromkeys((*DEFAULT_PRELOAD, *modules)))
    ISOLATED_CONTEXT.set_forkserver_preload(preload)


def warm_forkserver() -> None:
    """Start the forkserver now, so it imports the preload before the first parse.

    It blocks until the server is accepting work, which includes the preload imports
    (seconds for a service graph): call it off the event loop (``asyncio.to_thread``).
    Under ``spawn``, or with the server already running, it does nothing.
    """
    if ISOLATED_CONTEXT.get_start_method() != "forkserver":
        return
    forkserver.ensure_running()
