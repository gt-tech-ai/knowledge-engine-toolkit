"""``StubTokenLedger`` — the default ledger: allows every call, records nothing, reports zeros."""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    Period,
    TokenLedger,
    UsageRecord,
    UsageScope,
    UsageSummary,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


class StubTokenLedger(NoOpAsyncResource, TokenLedger):
    """Allow-all, record-nothing ledger (``kind="stub"``), so the graph boots with no Redis or service."""

    @override
    async def check_budget(self, scope: UsageScope) -> BudgetDecision:
        """Allow the call; ``reason="shadow"`` marks that nothing is enforced."""
        return BudgetDecision(
            allowed=True, reason="shadow", remaining_tokens=None, remaining_cost_usd=None
        )

    async def record(self, usage: UsageRecord) -> None:
        """Discard ``usage``."""

    @override
    async def usage(self, scope: UsageScope, period: Period) -> UsageSummary:
        """Return an all-zero summary for ``scope`` over ``period``."""
        return UsageSummary(scope=scope, period=period)


_INTERFACE_CHECK: type[TokenLedger] = StubTokenLedger
"""Type-checker assertion that ``StubTokenLedger`` satisfies ``TokenLedger``."""
