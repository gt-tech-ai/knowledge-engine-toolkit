"""The service custom-op chain (mirrors Go ``service.OpChain``).

A service's domain operations (anything beyond CRUD) run through
``exec_op(service_op_chain(name, deps), op, fn)``, so they get ServiceBuilder's stack in
ServiceBuilder's order — outermost first: recovery → tracing → metrics → authorization →
logging → timeout — with the same action strings (``<name>.<op>``), span names
(``service.<op>``) and metric labels (``service``, ``op``) as the CRUD ops. Recovery is always
present.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.foundation.decorate import (
    chain,
    new_auth,
    new_logging,
    new_metrics,
    new_recovery,
    new_timeout,
    new_tracing,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.tracer import TracerProvider
    from techai_webutils.foundation.decorate import OpMiddleware


@dataclass(frozen=True)
class ServiceOpDeps:
    """The collaborators a service's domain ops are wrapped with; ``None`` skips a layer.

    Pass the same ``authorize`` and instruments given to ServiceBuilder so domain ops and CRUD
    ops share one policy and one set of series.
    """

    logger: Logger | None = None
    tracer: TracerProvider | None = None
    histogram: MetricHistogram | None = None
    executions: MetricCounter | None = None
    errors: MetricCounter | None = None
    authorize: Callable[[str], Awaitable[bool]] | None = None
    timeout: float | None = None


def service_op_chain(name: str, deps: ServiceOpDeps) -> OpMiddleware:
    """Build the custom-op chain for the service named ``name``."""
    mws: list[OpMiddleware] = [new_recovery(deps.logger, name)]
    if deps.tracer is not None:
        mws.append(
            new_tracing(deps.tracer, name, span_prefix="service", subject_attr="service")
        )
    if deps.histogram is not None:
        mws.append(
            new_metrics(
                name,
                deps.histogram,
                subject_label="service",
                executions=deps.executions,
                errors=deps.errors,
            )
        )
    if deps.authorize is not None:
        mws.append(new_auth(name, deps.authorize))
    if deps.logger is not None:
        mws.append(
            new_logging(deps.logger, name, subject_field="service", msg_prefix="service")
        )
    if deps.timeout is not None:
        mws.append(new_timeout(deps.timeout, msg_prefix="service"))
    return chain(*mws)
