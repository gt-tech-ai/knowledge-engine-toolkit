"""``StubTokenLedger`` — the default ledger: allows every call, records nothing.

Reports zeros.
"""

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
    """Allow-all, record-nothing ledger (``kind="stub"``).

    So the graph boots with no Redis or service.
    """

    @override
    async def check_budget(self, scope: UsageScope) -> BudgetDecision:
        """Allow the call; ``reason="shadow"`` marks that nothing is enforced.

        Args:
            scope: The org, workspace and user making the call.

        Returns:
            An allowing decision with reason ``shadow``.

        """
        return BudgetDecision(
            allowed=True, reason="shadow", remaining_tokens=None, remaining_cost_usd=None
        )

    @override
    async def record(self, usage: UsageRecord) -> None:
        """Discard ``usage``.

        Args:
            usage: One call's tokens and cost.

        """

    @override
    async def usage(self, scope: UsageScope, period: Period) -> UsageSummary:
        """Return an all-zero summary for ``scope`` over ``period``.

        Args:
            scope: The org, workspace and user to total.
            period: The window to total over.

        Returns:
            An all-zero summary for ``scope`` over ``period``.

        """
        return UsageSummary(scope=scope, period=period)


_INTERFACE_CHECK: type[TokenLedger] = StubTokenLedger
"""Type-checker assertion that ``StubTokenLedger`` satisfies ``TokenLedger``."""
