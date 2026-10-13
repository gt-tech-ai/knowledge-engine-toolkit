"""Tests for foundation.decorate: the custom-operation decoration seam (Go ``foundation/decorate``)."""

from __future__ import annotations

import asyncio
from contextlib import contextmanager
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    ConflictError,
    ForbiddenError,
    InternalError,
    UnavailableError,
)
from techai_webutils.foundation.decorate import (
    chain,
    exec_op,
    new_auth,
    new_circuit_breaker,
    new_logging,
    new_metrics,
    new_recovery,
    new_retry,
    new_timeout,
    new_tracing,
)
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from collections.abc import Generator


def _recorder(calls: list[str], label: str) -> object:
    """Return a middleware that appends ``label`` on entry, then runs the operation."""

    class _Mw:
        async def wrap_op(self, op: str, next_: object) -> object:  # noqa: ARG002
            calls.append(label)
            return await next_()  # type: ignore[operator]

    return _Mw()


@pytest.mark.asyncio
async def test_exec_op_returns_the_operation_result_through_the_chain() -> None:
    """exec_op runs the operation under the chain and returns its typed result.

    **Why this test is important:**
      - Every custom repository/service op relies on exec_op carrying its result out unchanged

    **What it tests:**
      - The result is returned as-is and the chain ran first-outermost
    """
    calls: list[str] = []

    op = AsyncMock(side_effect=lambda: (calls.append("op"), {"n": 1})[-1])

    mw = chain(_recorder(calls, "outer"), _recorder(calls, "inner"))  # type: ignore[arg-type]
    assert await exec_op(mw, "get_by_slug", op) == {"n": 1}
    assert calls == ["outer", "inner", "op"]


@pytest.mark.asyncio
async def test_empty_chain_runs_the_operation() -> None:
    """chain() with no middlewares is a pass-through.

    **Why this test is important:**
      - A composition root with every collaborator absent must still run the op

    **What it tests:**
      - The op result is returned
    """

    op = AsyncMock(return_value=7)

    assert await exec_op(chain(), "op", op) == 7


@pytest.mark.asyncio
async def test_recovery_codes_unexpected_errors_and_passes_coded_ones() -> None:
    """Recovery turns an uncoded exception into InternalError and logs it; AppError passes.

    **Why this test is important:**
      - The service tier must never leak a raw exception past its boundary

    **What it tests:**
      - ValueError → InternalError (logged); ConflictError is re-raised unchanged
    """
    logger = MagicMock()
    mw = new_recovery(logger, "projects")

    boom = AsyncMock(side_effect=ValueError("boom"))

    conflict = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(InternalError):
        await exec_op(mw, "archive", boom)
    logger.exception.assert_called_once()
    with pytest.raises(ConflictError):
        await exec_op(mw, "archive", conflict)


@pytest.mark.asyncio
async def test_recovery_without_logger_still_codes() -> None:
    """Recovery with no logger still codes the error.

    **Why this test is important:**
      - Recovery is unconditional; a missing logger must not disable it

    **What it tests:**
      - InternalError is raised
    """

    boom = AsyncMock(side_effect=RuntimeError)

    with pytest.raises(InternalError):
        await exec_op(new_recovery(None, "projects"), "archive", boom)


@pytest.mark.asyncio
async def test_tracing_names_the_span_and_records_errors() -> None:
    """Tracing opens ``<prefix>.<op>`` tagged with the subject and records a failure.

    **Why this test is important:**
      - Custom ops must appear in traces exactly like the tier's CRUD ops

    **What it tests:**
      - Span name and attributes; record_error on failure
    """
    span = MagicMock()
    tracer = MagicMock()

    @contextmanager
    def _span(name: str, **attrs: object) -> Generator[MagicMock]:
        span.opened = (name, attrs)
        yield span

    tracer.span.side_effect = _span
    mw = new_tracing(tracer, "projects", span_prefix="repo", subject_attr="repo")

    ok = AsyncMock(return_value="ok")

    assert await exec_op(mw, "get_by_slug", ok) == "ok"
    assert span.opened == ("repo.get_by_slug", {"repo": "projects"})

    fail = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(ConflictError):
        await exec_op(mw, "get_by_slug", fail)
    span.record_error.assert_called_once()


@pytest.mark.asyncio
async def test_metrics_observe_duration_and_count_errors() -> None:
    """Metrics count every call, count failures, and observe duration with subject/op labels.

    **Why this test is important:**
      - The same instruments serve the CRUD decorators, so labels must match theirs

    **What it tests:**
      - executions/errors/histogram called with {subject: name, op: op}
    """
    hist, execs, errs = MagicMock(), MagicMock(), MagicMock()
    mw = new_metrics(
        "projects", hist, subject_label="repo", executions=execs, errors=errs
    )

    ok = AsyncMock(return_value=1)

    fail = AsyncMock(side_effect=ConflictError("dup"))

    await exec_op(mw, "get_by_slug", ok)
    with pytest.raises(ConflictError):
        await exec_op(mw, "get_by_slug", fail)
    labels = {"repo": "projects", "op": "get_by_slug"}
    assert execs.inc.call_count == 2
    execs.inc.assert_called_with(1.0, **labels)
    errs.inc.assert_called_once_with(1.0, **labels)
    assert hist.observe.call_count == 2
    assert hist.observe.call_args.kwargs == labels


@pytest.mark.asyncio
async def test_metrics_histogram_only() -> None:
    """Metrics with only a histogram observes duration and needs no counters.

    **Why this test is important:**
      - Counters are optional, as in RepositoryBuilder.with_metrics

    **What it tests:**
      - observe is called once
    """
    hist = MagicMock()

    fail = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(ConflictError):
        await exec_op(new_metrics("p", hist, subject_label="service"), "archive", fail)
    hist.observe.assert_called_once()


@pytest.mark.asyncio
async def test_logging_logs_entry_and_failure_at_debug() -> None:
    """Logging logs ``<prefix>.<op>`` on entry and ``... failed`` on failure, both at Debug.

    **Why this test is important:**
      - Inner layers log at Debug so the outermost seam owns Error-level logging

    **What it tests:**
      - Two debug calls, no error/exception call
    """
    logger = MagicMock()
    mw = new_logging(logger, "projects", subject_field="service", msg_prefix="service")

    fail = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(ConflictError):
        await exec_op(mw, "archive", fail)
    messages = [c.args[0] for c in logger.debug.call_args_list]
    assert messages == ["service.archive", "service.archive failed"]
    assert logger.debug.call_args_list[0].kwargs["service"] == "projects"
    logger.exception.assert_not_called()
    logger.error.assert_not_called()


@pytest.mark.asyncio
async def test_auth_checks_the_action_and_denies() -> None:
    """Auth asks auth_fn for ``<name>.<op>`` and raises ForbiddenError when refused.

    **Why this test is important:**
      - Custom service ops must be authorized exactly like the CRUD ops

    **What it tests:**
      - The action string; allow runs the op; deny skips it
    """
    auth_fn = AsyncMock(side_effect=[True, False])
    op = AsyncMock(return_value="done")
    mw = new_auth("projects", auth_fn)

    assert await exec_op(mw, "archive", op) == "done"
    auth_fn.assert_awaited_with("projects.archive")
    with pytest.raises(ForbiddenError):
        await exec_op(mw, "archive", op)
    assert op.await_count == 1


@pytest.mark.asyncio
async def test_timeout_raises_app_timeout() -> None:
    """Timeout bounds the op and raises AppTimeoutError naming the op.

    **Why this test is important:**
      - Every external call needs a deadline that surfaces as a coded, transient error

    **What it tests:**
      - A slow op raises AppTimeoutError with "<prefix>.<op>" in the message
    """

    async def slow() -> None:
        await asyncio.sleep(1)

    with pytest.raises(AppTimeoutError, match=r"repo\.get_by_slug timed out"):
        await exec_op(new_timeout(0.01, msg_prefix="repo"), "get_by_slug", slow)


@pytest.mark.asyncio
async def test_retry_runs_the_op_through_the_retrier() -> None:
    """Retry delegates the op to the injected Retrier.

    **Why this test is important:**
      - Retry policy lives in the Retrier; the middleware must hand it the whole op

    **What it tests:**
      - retrier.retry is awaited and its result returned
    """
    retrier = MagicMock()

    async def _retry(op: object) -> object:
        return await op()  # type: ignore[operator]

    retrier.retry = AsyncMock(side_effect=_retry)

    op = AsyncMock(return_value="ok")

    assert await exec_op(new_retry(retrier), "get_by_slug", op) == "ok"
    retrier.retry.assert_awaited_once()


@pytest.mark.asyncio
async def test_circuit_breaker_codes_an_open_circuit_unavailable() -> None:
    """An open breaker surfaces as UnavailableError; the op's own error passes with its code.

    **Why this test is important:**
      - Only the breaker's rejection is transient; the op's error must keep its code

    **What it tests:**
      - CircuitOpenError → UnavailableError; ConflictError unchanged
    """
    cb = MagicMock()
    mw = new_circuit_breaker(cb)

    open_ = AsyncMock(side_effect=CircuitOpenError("open"))

    conflict = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(UnavailableError):
        await exec_op(mw, "get", open_)
    with pytest.raises(ConflictError):
        await exec_op(mw, "get", conflict)
    assert cb.__enter__.call_count == 2
