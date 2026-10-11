"""``TokenLedger`` — the swappable contract for AI token usage, cost and budgets.

Every model call is recorded against a ``UsageScope`` (an organization, optionally narrowed to a
team, workspace and user); a caller asks ``check_budget`` before a call and ``record``s what it used
after. Backends honour two semantics:

- **Fail-open.** A ledger outage never blocks a model call: ``check_budget`` answers
  ``allowed=True`` with a reason naming the outage, and ``record`` logs and counts a failure
  instead of raising.
- **At-least-once.** A recorded usage may be delivered to the durable ledger more than once; each
  ``UsageRecord`` carries the ids (``trace_id`` + ``ts``) a durable consumer de-duplicates on.

Money is ``Decimal`` throughout, never ``float``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from decimal import Decimal
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from datetime import datetime


class Period(StrEnum):
    """The window a usage summary or a budget covers."""

    DAILY = "daily"
    """The current UTC calendar day."""
    MONTHLY = "monthly"
    """The current UTC calendar month."""
    ROLLING_30D = "rolling_30d"
    """The trailing 30 days."""


@dataclass(frozen=True, slots=True)
class UsageScope:
    """Who a usage is charged to: an organization, optionally narrowed further."""

    org_id: str
    """The organization (required; every counter and budget is keyed by it)."""
    team_id: str | None = None
    """The team within the organization, when known."""
    workspace_id: str | None = None
    """The workspace the call ran in, when known."""
    user_id: str | None = None
    """The end user, when known."""

    def __post_init__(self) -> None:
        """Reject a scope with no organization (``INVALID_INPUT``)."""
        if not self.org_id:
            raise AppError(ErrorCode.INVALID_INPUT, "usage scope requires an org_id")


@dataclass(frozen=True, slots=True, kw_only=True)
class UsageRecord:
    """One model call's token usage and cost, as recorded against a scope."""

    scope: UsageScope
    """Who the usage is charged to."""
    model: str
    """The model that served the call (the price-table key)."""
    operation: str
    """What the call was for (``generate``, ``rewrite``, ``embed``, …)."""
    input_tokens: int
    """Prompt tokens."""
    output_tokens: int
    """Completion tokens."""
    ts: datetime
    """When the call happened (timezone-aware)."""
    embed_tokens: int = 0
    """Embedding tokens (embedding calls)."""
    cost_usd: Decimal = field(default_factory=lambda: Decimal(0))
    """The call's cost in US dollars (``cost_of`` against the price table)."""
    pricing_version: str = ""
    """Version of the price table ``cost_usd`` was computed with."""
    trace_id: str = ""
    """The call's trace id (hex), for correlation and de-duplication."""


@dataclass(frozen=True, slots=True)
class BudgetDecision:
    """Whether a scope may make another model call, and how much budget is left."""

    allowed: bool
    """True when the call may proceed (always True when the ledger fails open)."""
    reason: str
    """Why: ``within_budget``, ``budget_exhausted``, ``shadow``, ``ledger_unavailable_fail_open``, …"""
    remaining_tokens: int | None
    """Tokens left in the window, or ``None`` when no token limit applies."""
    remaining_cost_usd: Decimal | None
    """Dollars left in the window, or ``None`` when no cost limit applies."""


@dataclass(frozen=True, slots=True)
class UsageSummary:
    """Aggregated usage of a scope over a period."""

    scope: UsageScope
    """The scope summarized."""
    period: Period
    """The window summarized."""
    input_tokens: int = 0
    """Total prompt tokens."""
    output_tokens: int = 0
    """Total completion tokens."""
    embed_tokens: int = 0
    """Total embedding tokens."""
    cost_usd: Decimal = field(default_factory=lambda: Decimal(0))
    """Total cost in US dollars."""


class TokenLedger(ManagedResource, ABC):
    """Records AI token usage and answers budget questions (fail-open, at-least-once).

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): the Redis backend owns a
    client and a remote backend a channel, so a ledger is an async context manager.
    """

    @abstractmethod
    async def check_budget(self, scope: UsageScope) -> BudgetDecision:
        """Return whether ``scope`` may make another call; never raises for a ledger outage."""

    @abstractmethod
    async def record(self, usage: UsageRecord) -> None:
        """Record one call's usage; best-effort, never raises for a ledger outage."""

    @abstractmethod
    async def usage(self, scope: UsageScope, period: Period) -> UsageSummary:
        """Return ``scope``'s aggregated usage over ``period``."""
