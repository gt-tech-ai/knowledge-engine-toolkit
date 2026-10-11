"""``RedisTokenLedger`` — the hot-path ``TokenLedger`` over the app's async Redis client.

Layout (``<prefix>`` is ``TokenLedgerConfig.redis_prefix``, ``token_ledger`` by default; a scope with
no workspace uses ``-``):

- **Counters** — a hash per scope and window, ``<prefix>:{org}:{workspace}:{window}``, with integer
  fields ``input_tokens``, ``output_tokens``, ``embed_tokens`` and ``cost_micro_usd``. ``{window}`` is
  the UTC month (``2026-10``) for a monthly ledger and the UTC day (``2026-10-09``) for a daily or
  rolling-30-day one (rolling sums the last 30 daily hashes). Each hash expires at its window's end
  (a daily hash kept for rolling expires 30 days later).
- **Limits** — a hash ``<prefix>:limits:{org}:{workspace}`` with optional fields ``tokens`` (integer)
  and ``cost_usd`` (decimal string), written by the durable ledger's owner. A missing field is no limit.
- **Usage stream** — ``TokenLedgerConfig.stream``; one entry per record with the fields ``org_id,
  team_id, workspace_id, user_id, model, operation, input_tokens, output_tokens, embed_tokens,
  cost_usd`` (decimal string), ``pricing_version, trace_id, ts`` (RFC 3339 UTC), trimmed with
  ``XADD MAXLEN ~``. The durable ledger drains it (at-least-once).

``record`` is one Lua script (counters + expiry + stream entry in one atomic round-trip; redis-py
runs it with ``EVALSHA`` and loads it on a cache miss). Because the script touches a per-scope key
and the shared stream, it needs a non-cluster Redis (or a cluster where both hash to one slot).
``record`` and ``check_budget`` fail open (a Redis error or an unparsable limits hash allows the
call); the cost counter is the record's cost rounded half-even to the micro-dollar, as ``cost_of``
rounds it; ``usage`` is an approximate snapshot of the counters — the
authoritative aggregate is the durable ledger behind a consumer-supplied kind.
"""

from __future__ import annotations

from calendar import monthrange
from datetime import UTC, datetime, timedelta
from decimal import ROUND_HALF_EVEN, Decimal
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.token_ledger.cost import MICRO_DOLLAR
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    Period,
    TokenLedger,
    UsageRecord,
    UsageScope,
    UsageSummary,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from redis.asyncio import Redis

    from techai_webutils.clients.token_ledger.builder import TokenLedgerConfig
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider

COUNTER_FIELDS = ("input_tokens", "output_tokens", "embed_tokens", "cost_micro_usd")
"""The integer fields of a counter hash, in the order the script increments and reads them."""

_MICROS = Decimal(1_000_000)
"""Micro-dollars per dollar (the cost counter is an integer of micro-dollars)."""

_ROLLING_DAYS = 30
"""Days summed by a rolling-30-day ledger."""

_NO_WORKSPACE = "-"
"""Key segment for a scope without a workspace."""

RECORD_SCRIPT = """
redis.call('HINCRBY', KEYS[1], 'input_tokens', ARGV[3])
redis.call('HINCRBY', KEYS[1], 'output_tokens', ARGV[4])
redis.call('HINCRBY', KEYS[1], 'embed_tokens', ARGV[5])
redis.call('HINCRBY', KEYS[1], 'cost_micro_usd', ARGV[6])
redis.call('EXPIREAT', KEYS[1], ARGV[1])
local fields = {}
for i = 7, #ARGV do fields[#fields + 1] = ARGV[i] end
return redis.call('XADD', KEYS[2], 'MAXLEN', '~', ARGV[2], '*', unpack(fields))
"""
"""Atomically increment a scope's window counters, set their expiry and append the usage entry."""


def _utcnow() -> datetime:
    """Return the current UTC time (the window clock; patched in tests)."""
    return datetime.now(UTC)


def _text(value: bytes | str | None) -> str | None:
    """Decode a Redis reply value (bytes when ``decode_responses`` is off)."""
    return value.decode() if isinstance(value, bytes) else value


def _rfc3339(ts: datetime) -> str:
    """Render ``ts`` in UTC as RFC 3339 with a ``Z`` suffix."""
    return ts.astimezone(UTC).isoformat().replace("+00:00", "Z")


class RedisTokenLedger(NoOpAsyncResource, TokenLedger):
    """``TokenLedger`` on Redis counters and a usage stream (``kind="redis"``).

    The Redis client is the app's own (injected); its lifecycle belongs to the composition root, so
    this ledger's async context is a no-op.
    """

    def __init__(
        self,
        redis: Redis,
        config: TokenLedgerConfig,
        *,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Bind the client and config, register the record script and the failure counter.

        Raises:
            AppError: ``INVALID_INPUT`` when ``config.window`` is not a ``Period`` value (a plain
                string such as ``"rolling_30d"`` from YAML is accepted and coerced).

        """
        try:
            self._window = Period(config.window)
        except ValueError as exc:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                f"unknown token ledger window {config.window!r}",
                cause=exc,
            ) from exc
        self._redis = redis
        self._config = config
        self._script = redis.register_script(RECORD_SCRIPT)
        self._logger = get_logger("token_ledger")
        self._failed: MetricCounter | None = None
        if metrics is not None:
            self._failed = metrics.counter(
                "token_ledger_record_failed_total",
                "Token usage records that failed to reach the ledger.",
                [],
            )

    @staticmethod
    def _workspace(scope: UsageScope) -> str:
        """Return the key segment for ``scope``'s workspace."""
        return scope.workspace_id or _NO_WORKSPACE

    def _counter_key(self, scope: UsageScope, window: str) -> str:
        """Return the counter hash key for ``scope`` and window id ``window``."""
        return f"{self._config.redis_prefix}:{scope.org_id}:{self._workspace(scope)}:{window}"

    def _limits_key(self, scope: UsageScope) -> str:
        """Return the limits hash key for ``scope``."""
        return (
            f"{self._config.redis_prefix}:limits:{scope.org_id}:{self._workspace(scope)}"
        )

    def _monthly(self) -> bool:
        """Return True when counters are kept per month (else per day)."""
        return self._window == Period.MONTHLY

    def _bucket(self, ts: datetime) -> tuple[str, int]:
        """Return the window id ``ts`` falls in and that bucket's expiry (epoch seconds)."""
        utc = ts.astimezone(UTC)
        if self._monthly():
            days = monthrange(utc.year, utc.month)[1]
            start = datetime(utc.year, utc.month, 1, tzinfo=UTC)
            return utc.strftime("%Y-%m"), int((start + timedelta(days=days)).timestamp())
        day_end = datetime(utc.year, utc.month, utc.day, tzinfo=UTC) + timedelta(days=1)
        keep = (
            timedelta(days=_ROLLING_DAYS)
            if self._window == Period.ROLLING_30D
            else timedelta(0)
        )
        return utc.strftime("%Y-%m-%d"), int((day_end + keep).timestamp())

    def _window_keys(self, scope: UsageScope, now: datetime) -> list[str]:
        """Return the counter keys the current window sums (one, or 30 days for rolling)."""
        if self._window == Period.ROLLING_30D:
            return [
                self._counter_key(
                    scope, (now - timedelta(days=i)).astimezone(UTC).strftime("%Y-%m-%d")
                )
                for i in range(_ROLLING_DAYS)
            ]
        return [self._counter_key(scope, self._bucket(now)[0])]

    async def record(self, usage: UsageRecord) -> None:
        """Increment the window counters and append the usage entry in one script; never raises."""
        window, expire_at = self._bucket(usage.ts)
        scope = usage.scope
        entry = {
            "org_id": scope.org_id,
            "team_id": scope.team_id or "",
            "workspace_id": scope.workspace_id or "",
            "user_id": scope.user_id or "",
            "model": usage.model,
            "operation": usage.operation,
            "input_tokens": str(usage.input_tokens),
            "output_tokens": str(usage.output_tokens),
            "embed_tokens": str(usage.embed_tokens),
            "cost_usd": str(usage.cost_usd),
            "pricing_version": usage.pricing_version,
            "trace_id": usage.trace_id,
            "ts": _rfc3339(usage.ts),
        }
        args: list[str | int] = [
            expire_at,
            self._config.stream_maxlen,
            usage.input_tokens,
            usage.output_tokens,
            usage.embed_tokens,
            int(
                usage.cost_usd.quantize(MICRO_DOLLAR, rounding=ROUND_HALF_EVEN) * _MICROS
            ),
        ]
        for key, value in entry.items():
            args += [key, value]
        try:
            await self._script(
                keys=[self._counter_key(scope, window), self._config.stream], args=args
            )
        except Exception:
            if self._failed is not None:
                self._failed.inc()
            self._logger.warning(
                "token usage record failed", org_id=scope.org_id, exc_info=True
            )

    async def _read(self, keys: list[str], limits_key: str | None) -> list[object]:
        """Read the counter hashes (and the limits hash) in one non-transactional pipeline."""
        async with self._redis.pipeline(transaction=False) as pipe:
            for key in keys:
                pipe.hmget(key, *COUNTER_FIELDS)
            if limits_key is not None:
                pipe.hgetall(limits_key)
            return await pipe.execute()

    @staticmethod
    def _sum(rows: list[object]) -> tuple[int, int, int, int]:
        """Sum counter rows (``HMGET`` replies) field by field; a missing field counts as 0."""
        totals = [0, 0, 0, 0]
        for row in rows:
            for i, value in enumerate(cast("list[bytes | str | None]", row)):
                text = _text(value)
                totals[i] += int(text) if text else 0
        return totals[0], totals[1], totals[2], totals[3]

    async def check_budget(self, scope: UsageScope) -> BudgetDecision:
        """Compare the window's counters with the scope's limits; fail open on a Redis or parse error."""
        keys = self._window_keys(scope, _utcnow())
        try:
            replies = await self._read(keys, self._limits_key(scope))
            return self._decide(replies)
        except Exception:
            self._logger.warning(
                "token budget check failed open", org_id=scope.org_id, exc_info=True
            )
            return BudgetDecision(
                allowed=True,
                reason="ledger_unavailable_fail_open",
                remaining_tokens=None,
                remaining_cost_usd=None,
            )

    def _decide(self, replies: list[object]) -> BudgetDecision:
        """Return the budget decision for the counter rows plus the trailing limits hash reply.

        Raises:
            ValueError: A counter or limit value is not a number (the caller fails open).
            decimal.InvalidOperation: ``cost_usd`` is not a decimal (the caller fails open).

        """
        input_tokens, output_tokens, embed_tokens, cost_micros = self._sum(replies[:-1])
        limits = {_text(k): _text(v) for k, v in dict(replies[-1]).items()}  # type: ignore[call-overload]
        token_limit = limits.get("tokens")
        cost_limit = limits.get("cost_usd")
        if token_limit is None and cost_limit is None:
            return BudgetDecision(
                allowed=True,
                reason="no_limit",
                remaining_tokens=None,
                remaining_cost_usd=None,
            )
        remaining_tokens = None
        remaining_cost = None
        if token_limit is not None:
            remaining_tokens = max(
                int(token_limit) - (input_tokens + output_tokens + embed_tokens), 0
            )
        if cost_limit is not None:
            spent = Decimal(cost_micros) / _MICROS
            remaining_cost = max(Decimal(cost_limit) - spent, Decimal(0)).quantize(
                Decimal("0.000001")
            )
        exhausted = remaining_tokens == 0 or remaining_cost == 0
        return BudgetDecision(
            allowed=not exhausted,
            reason="budget_exhausted" if exhausted else "within_budget",
            remaining_tokens=remaining_tokens,
            remaining_cost_usd=remaining_cost,
        )

    async def usage(self, scope: UsageScope, period: Period) -> UsageSummary:
        """Return an approximate summary from the counters of the configured window.

        Only ``period == config.window`` is answerable from the hot-path counters; any other period
        raises ``INVALID_INPUT`` (the authoritative aggregate is the durable ledger's). A Redis error
        raises ``UNAVAILABLE``.
        """
        if period != self._window:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                f"redis token ledger keeps {self._window} counters; cannot answer {period}",
            )
        try:
            replies = await self._read(self._window_keys(scope, _utcnow()), None)
        except Exception as exc:
            raise AppError(
                ErrorCode.UNAVAILABLE, "token ledger counters unavailable", cause=exc
            ) from exc
        input_tokens, output_tokens, embed_tokens, cost_micros = self._sum(replies)
        return UsageSummary(
            scope=scope,
            period=period,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            embed_tokens=embed_tokens,
            cost_usd=(Decimal(cost_micros) / _MICROS).quantize(Decimal("0.000001")),
        )
