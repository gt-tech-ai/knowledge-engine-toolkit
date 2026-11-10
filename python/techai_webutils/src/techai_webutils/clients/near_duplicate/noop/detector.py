"""``NoopDetector``: near-duplicate detection off.

Every check returns ``None`` and stores nothing.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.near_duplicate import (
    NearDuplicate,
    NearDuplicateDetector,
)


class NoopDetector(NearDuplicateDetector):
    """Detects nothing, at no cost."""

    @override
    async def check(
        self, scope: str, document_id: str, text: str, *, ref: str = ""
    ) -> NearDuplicate | None:
        """Return None: detection is off.

        Args:
            scope: Unused.
            document_id: Unused.
            text: Unused.
            ref: Unused.

        Returns:
            None.

        """
        return None
