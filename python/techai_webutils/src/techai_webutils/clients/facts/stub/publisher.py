"""``StubFactPublisher`` — the default fact backend: accepts and discards every fact."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.types.fact import Fact


class StubFactPublisher(NoOpAsyncResource, FactPublisher):
    """Accepts facts and discards them, so the graph boots with no queue (selected by ``kind="stub"``)."""

    def publish(self, facts: Sequence[Fact]) -> None:
        """Discard ``facts``."""
