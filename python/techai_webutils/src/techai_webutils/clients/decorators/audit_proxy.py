"""``AuditProxy`` — safety chains around a model call and one ``AuditRecord`` per answer.

A transparent ``__getattr__`` proxy over an ``LLMProvider`` (or gateway). For
``complete``:

1. the **input** ``SafetyCheck`` scans the last user message: ``block`` → the request is
   audited as ``blocked`` and ``blocked_error`` (``FORBIDDEN``) raises before the model is
   called; ``redact`` → the redacted message is what the model receives;
2. the inner call runs;
3. the **output** check scans the answer: ``redact`` → the redacted answer is returned;
   ``block`` → audited as ``blocked``, then ``FORBIDDEN``;
4. one ``AuditRecord`` is built — the request context from the injected
   ``record_context``, the live ``trace_id``, the model (the route decision's, else the
   response's), tokens, PII and safety flags, the decision and ``cache_hit=False`` — and
   awaited on the ``AuditSink``.

Streams scan the prompt before opening and audit on exhaustion over the accumulated text;
an output ``block`` can't retract tokens already sent, so it is recorded
``flagged_after_stream``. ``audit_cached`` is the semantic cache's ``on_hit`` hook, run
before a hit is served: both chains scan it, it is audited with ``cache_hit=True`` (an
input ``redact`` records the redacted prompt, as a live call does), an output ``redact``
returns the redacted answer for the cache to serve and a ``block`` raises ``FORBIDDEN``
(the hit is not served). A record carries the route decision's model only when the audited
call itself set that decision (a gateway beneath the proxy); a plain provider, a blocked
input or a cache hit records the model it knows (none, or the answer's).

``AuditEnforcement`` governs failures of the audit itself: ``shadow`` logs and counts
(``audit_record_failures_total{enforcement}``) and serves; ``enforced`` raises — a coded
error from the sink or a safety check keeps its code, any other is ``INTERNAL``. Under
shadow a safety scan that fails is recorded ``flagged``. Whether a detection blocks is the
safety check's own ``on_detect`` policy.
"""

from __future__ import annotations

import functools
from dataclasses import dataclass, replace
from datetime import UTC, datetime
from enum import StrEnum
from typing import TYPE_CHECKING, cast, final

from opentelemetry import trace

from techai_webutils.clients.decorators.ai_enricher import GEN_AI_SYSTEM_BY_PROVIDER
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.audit import AuditRecord
from techai_webutils.core.interfaces.llm import LLMMessage, LLMResponse, StreamUsage
from techai_webutils.core.interfaces.model_gateway import current_route_decision
from techai_webutils.core.interfaces.safety import (
    SafetyDecision,
    SafetyResult,
    ScanKind,
    blocked_error,
)
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from techai_webutils.core.interfaces.audit import AuditContext, AuditSink
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.model_gateway import RouteDecision
    from techai_webutils.core.interfaces.safety import SafetyCheck
    from techai_webutils.core.interfaces.semantic_cache import CachedAnswer

_CLEAN = SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)
"""The verdict of a scan that found nothing."""

_CHECK_FAILED = SafetyResult(
    flags=("safety.check_failed",), decision=SafetyDecision.ALLOW, redacted=None
)
"""The verdict recorded (under shadow) when a safety scan itself fails."""


class AuditEnforcement(StrEnum):
    """What a failure of the audit itself does."""

    SHADOW = "shadow"
    """Log and count; serve the answer (the default rollout stage)."""
    ENFORCED = "enforced"
    """Raise a coded error; an unaudited answer is not served."""


def _utcnow() -> datetime:
    """Return the current UTC time.

    Returns:
        The current UTC time.

    """
    return datetime.now(UTC)


def _trace_id() -> str:
    """Return the current span's trace id (hex), or ``""`` outside a recorded span.

    Returns:
        The current span's trace id (hex), or ``""`` outside a recorded span.

    """
    ctx = trace.get_current_span().get_span_context()
    return f"{ctx.trace_id:032x}" if ctx.is_valid else ""


def _messages(args: tuple[object, ...], kwargs: dict[str, object]) -> list[LLMMessage]:
    """Return a model call's ``messages`` argument.

    Returns:
        A model call's ``messages`` argument.

    """
    return cast("list[LLMMessage]", args[0] if args else kwargs.get("messages", []))


def _with_messages(
    args: tuple[object, ...], kwargs: dict[str, object], messages: list[LLMMessage]
) -> tuple[tuple[object, ...], dict[str, object]]:
    """Return the call's arguments with ``messages`` in place of the original.

    Returns:
        The call's arguments with ``messages`` in place of the original.

    """
    if args:
        return (messages, *args[1:]), kwargs
    return args, {**kwargs, "messages": messages}


def _last_user(messages: list[LLMMessage]) -> int | None:
    """Return the index of the last user message (what the input chain scans).

    Returns:
        The index of the last user message (what the input chain scans).

    """
    return next(
        (i for i in range(len(messages) - 1, -1, -1) if messages[i].role == "user"), None
    )


def _decision(results: tuple[SafetyResult, ...], *, block_label: str = "blocked") -> str:
    """Return the record's decision for the scans' verdicts.

    Returns:
        The record's decision for the scans' verdicts.

    """
    decisions = {result.decision for result in results}
    if SafetyDecision.BLOCK in decisions:
        return block_label
    return "redacted" if SafetyDecision.REDACT in decisions else "allowed"


@dataclass(frozen=True, kw_only=True)
class AuditProxyOptions:
    """What ``AuditProxy`` takes, grouped into one value."""

    sink: AuditSink
    """Appends the audit records."""

    input_check: SafetyCheck
    """Scans the prompt before the call."""

    output_check: SafetyCheck
    """Scans the answer after it."""

    enforcement: AuditEnforcement
    """What an audit failure does."""

    step: str
    """The pipeline step (a log field)."""

    record_context: Callable[[], AuditContext]
    """Returns the current request's ``AuditContext``."""

    metrics: MetricsProvider | None = None
    """Emits ``audit_record_failures_total``; ``None`` emits nothing."""

    clock: Callable[[], datetime] = _utcnow
    """Returns the record's ``created_at`` (injected for tests)."""


@dataclass(frozen=True, kw_only=True)
class _AuditRecord:
    """What ``AuditProxy`` takes, grouped into one value."""

    prompt: str
    """The ``prompt`` value of ``AuditProxy``."""

    answer: str
    """The ``answer`` value of ``AuditProxy``."""

    model: str
    """The ``model`` value of ``AuditProxy``."""

    tokens: tuple[int, int]
    """The ``tokens`` value of ``AuditProxy``."""

    results: tuple[SafetyResult, ...]
    """The ``results`` value of ``AuditProxy``."""

    decision: str
    """The ``decision`` value of ``AuditProxy``."""

    cache_hit: bool
    """The ``cache_hit`` value of ``AuditProxy``."""

    routed: RouteDecision | None = None
    """The ``routed`` value of ``AuditProxy``."""


@final
class AuditProxy:
    """Runs the safety chains around a model call and records the answer for audit."""

    def __init__(self, inner: object, options: AuditProxyOptions) -> None:
        """Wrap ``inner`` for the pipeline step ``step``.

        Args:
            inner: The provider or gateway whose answers are audited.
            options: The settings and collaborators, grouped.

        """
        self._inner = inner
        self._sink = options.sink
        self._input = options.input_check
        self._output = options.output_check
        self._enforcement = options.enforcement
        self._step = options.step
        self._context = options.record_context
        self._clock = options.clock
        served = getattr(inner, "wrapped_provider", inner)
        self._provider = GEN_AI_SYSTEM_BY_PROVIDER.get(type(served).__name__, "unknown")
        self._logger = get_logger("audit_proxy")
        self._failures: MetricCounter | None = None
        if options.metrics is not None:
            self._failures = options.metrics.counter(
                "audit_record_failures_total",
                "Audit steps that failed, by enforcement.",
                ["enforcement"],
            )

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, audited when it is a model call.

        Returns:
            The inner attribute, audited when it is a model call.

        """
        attr = cast("object", getattr(self._inner, name))
        if not callable(attr):
            return attr
        if name == "complete":
            return self._wrap_complete(attr)
        if name in {"stream", "stream_with_usage"}:
            source = getattr(self._inner, "stream_with_usage", attr)
            return self._wrap_stream(attr, source, keep_usage=name == "stream_with_usage")
        return attr

    def _failed(self, what: str, error: Exception) -> None:
        """Raise ``error`` (coded) under enforcement; log and count it under shadow.

        Raises:
            AppError: under ``enforced``: the error itself when coded, else ``INTERNAL``.

        """
        if self._enforcement is AuditEnforcement.ENFORCED:
            if isinstance(error, AppError):
                raise error
            msg = f"audit {what} failed"
            raise AppError(ErrorCode.INTERNAL, msg, cause=error) from error
        if self._failures is not None:
            self._failures.inc(1.0, enforcement=str(self._enforcement))

    async def _scan(self, check: SafetyCheck, text: str, kind: ScanKind) -> SafetyResult:
        """Scan ``text``; a failed scan raises under enforcement and is flagged.

        Under shadow.

        Returns:
            The scan result, or the failed-check result under shadow.

        """
        try:
            return await check.scan(text, kind=kind)
        except Exception as exc:
            self._logger.exception("scan failed")
            self._failed("safety scan", exc)
            return _CHECK_FAILED

    async def _commit(self, record: AuditRecord) -> None:
        """Append ``record``; a sink failure raises under enforcement.

        Is counted under shadow.
        """
        try:
            await self._sink.record(record)
        except Exception as exc:
            self._logger.exception("commit failed")
            self._failed("record", exc)

    def _record(self, options: _AuditRecord) -> AuditRecord:
        """Return the audit record for one answer under the current request context.

        ``routed`` is the route decision the audited call itself set (``_routed_since``);
        its model, when set, is the record's model. A decision a previous step left behind
        is never passed, so it can't name a model that did not produce this answer.

        Returns:
            The audit record for one answer under the current request context.

        """
        ctx = self._context()
        flags = tuple(flag for result in options.results for flag in result.flags)
        return AuditRecord(
            request_id=ctx.request_id,
            trace_id=_trace_id(),
            tenant_id=ctx.tenant_id,
            workspace_id=ctx.workspace_id,
            user_id=ctx.user_id,
            clearance_level=ctx.clearance_level,
            model=options.routed.model_id
            if options.routed is not None and options.routed.model_id
            else options.model,
            provider=self._provider,
            prompt=options.prompt,
            answer=options.answer,
            sources=ctx.sources,
            prompt_tokens=options.tokens[0],
            completion_tokens=options.tokens[1],
            pii_flags=tuple(f for f in flags if f.startswith("pii.")),
            safety_flags=tuple(f for f in flags if not f.startswith("pii.")),
            decision=options.decision,
            cache_hit=options.cache_hit,
            created_at=self._clock(),
        )

    async def _guard_input(
        self, args: tuple[object, ...], kwargs: dict[str, object]
    ) -> tuple[tuple[object, ...], dict[str, object], str, SafetyResult]:
        """Scan the prompt; return the (possibly redacted) call arguments.

        Prompt and verdict.

        A callee raises ``AppError``: ``FORBIDDEN`` when the input check blocks (after
        auditing it).

        Returns:
            The possibly redacted arguments, the prompt and the scan result.

        Raises:
            blocked_error: The error built for a blocked prompt or answer, when enforcing.

        """
        messages = _messages(args, kwargs)
        index = _last_user(messages)
        prompt = messages[index].content if index is not None else ""
        result = await self._scan(self._input, prompt, ScanKind.PROMPT)
        if result.decision is SafetyDecision.BLOCK:
            await self._commit(
                self._record(
                    options=_AuditRecord(
                        prompt=prompt,
                        answer="",
                        model="",
                        tokens=(0, 0),
                        results=(result,),
                        decision="blocked",
                        cache_hit=False,
                    )
                )
            )
            raise blocked_error(result)
        if (
            result.decision is SafetyDecision.REDACT
            and index is not None
            and result.redacted is not None
        ):
            prompt = result.redacted
            messages = [
                *messages[:index],
                replace(messages[index], content=prompt),
                *messages[index + 1 :],
            ]
            args, kwargs = _with_messages(args, kwargs, messages)
        return args, kwargs, prompt, result

    def _wrap_complete(self, attr: Callable[..., object]) -> object:
        """Wrap ``complete``: input chain, call, output chain, audit.

        Returns:
            The wrapped ``complete``.

        """

        @functools.wraps(attr)
        async def complete(*args: object, **kwargs: object) -> LLMResponse:
            args, kwargs, prompt, before = await self._guard_input(args, kwargs)
            prior = current_route_decision()
            response = await cast("Callable[..., Awaitable[LLMResponse]]", attr)(
                *args, **kwargs
            )
            after = await self._scan(self._output, response.content, ScanKind.ANSWER)
            if after.decision is SafetyDecision.REDACT and after.redacted is not None:
                response = replace(response, content=after.redacted)
            blocked = after.decision is SafetyDecision.BLOCK
            await self._commit(
                self._record(
                    options=_AuditRecord(
                        prompt=prompt,
                        answer="" if blocked else response.content,
                        model=response.model,
                        tokens=(response.input_tokens, response.output_tokens),
                        results=(before, after),
                        decision=_decision((before, after)),
                        cache_hit=False,
                        routed=_routed_since(prior),
                    )
                )
            )
            if blocked:
                raise blocked_error(after)
            return response

        return complete

    def _wrap_stream(
        self,
        attr: Callable[..., object],
        source: Callable[..., object],
        *,
        keep_usage: bool,
    ) -> object:
        """Wrap a stream: input chain before opening.

        Output chain and audit at exhaustion.

        Returns:
            The wrapped streaming call.

        """

        @functools.wraps(attr)
        async def stream(
            *args: object, **kwargs: object
        ) -> AsyncIterator[str | StreamUsage]:
            args, kwargs, prompt, before = await self._guard_input(args, kwargs)
            opener = cast(
                "Callable[..., Awaitable[AsyncIterator[str | StreamUsage]]]", source
            )
            prior = current_route_decision()
            items = await opener(*args, **kwargs)
            return self._relay(items, prompt, before, prior, keep_usage=keep_usage)

        return stream

    async def _relay(
        self,
        items: AsyncIterator[str | StreamUsage],
        prompt: str,
        before: SafetyResult,
        prior: RouteDecision | None,
        *,
        keep_usage: bool,
    ) -> AsyncIterator[str | StreamUsage]:
        """Re-yield ``items``; once exhausted, scan the joined answer and audit it.

        ``prior`` is the route decision before the stream opened, so only a decision the
        stream itself set names the record's model.

        Yields:
            Each item of ``items``.

        """
        parts: list[str] = []
        usage: StreamUsage | None = None
        async for item in items:
            if isinstance(item, StreamUsage):
                usage = item
                if not keep_usage:
                    continue
            else:
                parts.append(item)
            yield item
        answer = "".join(parts)
        after = await self._scan(self._output, answer, ScanKind.ANSWER)
        await self._commit(
            self._record(
                options=_AuditRecord(
                    prompt=prompt,
                    answer=answer,
                    model=usage.model if usage is not None else "",
                    tokens=(usage.input_tokens, usage.output_tokens)
                    if usage is not None
                    else (0, 0),
                    results=(before, after),
                    decision=_decision(
                        (before, after), block_label="flagged_after_stream"
                    ),
                    cache_hit=False,
                    routed=_routed_since(prior),
                )
            )
        )

    async def audit_cached(
        self,
        args: tuple[object, ...],
        kwargs: dict[str, object],
        answer: object,
        cached: CachedAnswer,
    ) -> LLMResponse | None:
        """Scan and audit a hit before the semantic cache serves it (its ``on_hit`` hook).

        Returns: The redacted answer when the output check redacts, else ``None`` (serve
            as is).

        A callee raises ``AppError``: ``FORBIDDEN`` when either check blocks (after
        auditing it ``blocked``); under ``enforced``, a failed safety scan or record (a
        coded error keeps its code, any other is ``INTERNAL``), which the semantic cache
        treats as a refusal of the hit.

        Returns:
            The hit, redacted if the output chain requires it, or ``None`` when redaction
            empties it.

        Raises:
            blocked_error: The error built for a blocked prompt or answer, when enforcing.

        """
        del cached
        messages = _messages(args, kwargs)
        index = _last_user(messages)
        prompt = messages[index].content if index is not None else ""
        response = cast("LLMResponse", answer)
        before = await self._scan(self._input, prompt, ScanKind.PROMPT)
        if before.decision is SafetyDecision.REDACT and before.redacted is not None:
            prompt = before.redacted
        after = await self._scan(self._output, response.content, ScanKind.ANSWER)
        blocked = next(
            (r for r in (before, after) if r.decision is SafetyDecision.BLOCK), None
        )
        redacted = (
            after.redacted
            if after.decision is SafetyDecision.REDACT and after.redacted is not None
            else None
        )
        served = response.content if redacted is None else redacted
        await self._commit(
            self._record(
                options=_AuditRecord(
                    prompt=prompt,
                    answer="" if blocked is not None else served,
                    model=response.model,
                    tokens=(response.input_tokens, response.output_tokens),
                    results=(before, after),
                    decision=_decision((before, after)),
                    cache_hit=True,
                )
            )
        )
        if blocked is not None:
            raise blocked_error(blocked)
        return None if redacted is None else replace(response, content=redacted)


def _routed_since(prior: RouteDecision | None) -> RouteDecision | None:
    """Return the route decision set since ``prior`` was read, or ``None`` when none was.

    A gateway sets a fresh decision per call, so identity tells this call's decision from
    one a previous step left in the context, whatever decorators sit between the proxy and
    it.

    Returns:
        The route decision set since ``prior`` was read, or ``None`` when none was.

    """
    current = current_route_decision()
    return current if current is not None and current is not prior else None
