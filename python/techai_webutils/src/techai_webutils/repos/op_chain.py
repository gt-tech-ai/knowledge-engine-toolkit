"""The repository custom-op chain (mirrors Go ``repository.OpDeps`` + ``NewOpChain``).

A repository's non-CRUD queries run through ``exec_op(repo_op_chain(name, deps), op, fn)``,
so they get RepositoryBuilder's stack in RepositoryBuilder's order — outermost first:
logging → metrics → tracing → timeout → retry → circuit breaker — with the same span names
(``repo.<op>``), log messages and metric labels (``repo``, ``op``) as the CRUD ops.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.foundation.decorate import (
    chain,
    new_circuit_breaker,
    new_logging,
    new_metrics,
    new_retry,
    new_timeout,
    new_tracing,
)

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.core.interfaces.tracer import TracerProvider
    from techai_webutils.foundation.decorate import OpMiddleware


@dataclass(frozen=True)
class RepoOpDeps:
    """The collaborators a repository's custom ops are wrapped with; ``None`` skips a layer.

    Pass the same instruments given to ``RepositoryBuilder.with_metrics`` so custom ops and
    CRUD ops share one set of series.
    """

    logger: Logger | None = None
    tracer: TracerProvider | None = None
    histogram: MetricHistogram | None = None
    executions: MetricCounter | None = None
    errors: MetricCounter | None = None
    retrier: Retrier | None = None
    breaker: CircuitBreakerInterface | None = None
    timeout: float | None = None


def repo_op_chain(name: str, deps: RepoOpDeps) -> OpMiddleware:
    """Build the custom-op chain for the repository named ``name``."""
    mws: list[OpMiddleware] = []
    if deps.logger is not None:
        mws.append(
            new_logging(deps.logger, name, subject_field="repo", msg_prefix="repo")
        )
    if deps.histogram is not None:
        mws.append(
            new_metrics(
                name,
                deps.histogram,
                subject_label="repo",
                executions=deps.executions,
                errors=deps.errors,
            )
        )
    if deps.tracer is not None:
        mws.append(
            new_tracing(deps.tracer, name, span_prefix="repo", subject_attr="repo")
        )
    if deps.timeout is not None:
        mws.append(new_timeout(deps.timeout, msg_prefix="repo"))
    if deps.retrier is not None:
        mws.append(new_retry(deps.retrier))
    if deps.breaker is not None:
        mws.append(new_circuit_breaker(deps.breaker))
    return chain(*mws)
