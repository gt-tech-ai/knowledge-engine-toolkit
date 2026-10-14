"""Lays the controller tier out as a server framework's interceptors (mirrors Go ``transport.Interceptors``)."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.controllers.handler import tier
from techai_webutils.foundation.decorate import Layer, chain

if TYPE_CHECKING:
    from collections.abc import Callable, Sequence

    from techai_webutils.foundation.decorate import Deps, OpMiddleware


def lay_out[I](
    name: str,
    deps: Deps,
    native: Callable[[Layer], Sequence[I] | None],
    adapt: Callable[[OpMiddleware, OpMiddleware], I],
    authn: Sequence[I] = (),
) -> list[I]:
    """Return a framework's interceptors (type ``I``) for the controller ``name``, outermost first.

    An RPC server is decorated exactly like a ``ControllerBase`` handler:

    - A layer the framework implements natively — a trace continued from the request metadata,
      metrics labelled by status code, a status-aware access log — comes from ``native``, which
      returns its interceptors (empty when the collaborator is absent), or None when it does not
      handle the layer.
    - Every other run of consecutive layers becomes one interceptor, built by ``adapt`` from the
      run's middleware twice over: once for a unary call and once for a stream, which has no
      timeout so a long-lived stream outlives any call budget. A run whose layers all lack their
      collaborators adds nothing.
    - ``authn``, the framework's interceptors that authenticate the caller, run just before the
      auth layer, so it authorizes an authenticated principal.
    """
    controller = tier()
    stream_deps = deps.without_timeout()
    out: list[I] = []
    unary: list[OpMiddleware] = []
    stream: list[OpMiddleware] = []

    def flush() -> None:
        if unary or stream:
            out.append(adapt(chain(*unary), chain(*stream)))
        unary.clear()
        stream.clear()

    for layer in controller.order:
        if layer is Layer.AUTH:
            flush()
            out.extend(authn)
        own = native(layer)
        if own is not None:
            flush()
            out.extend(own)
            continue
        if (mw := controller.middleware(layer, name, deps)) is not None:
            unary.append(mw)
        if (mw := controller.middleware(layer, name, stream_deps)) is not None:
            stream.append(mw)
    flush()
    return out
