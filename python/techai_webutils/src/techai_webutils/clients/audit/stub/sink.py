"""``StubAuditSink`` — the default audit sink: appends each record to an observable in-memory list."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.audit import AuditRecord, AuditSink


class StubAuditSink(NoOpAsyncResource):
    """Keeps every record in ``records`` (``kind="stub"``), so the graph boots with no audit store.

    Records accumulate for the process's life; it is meant for dev, tests and smoke runs.
    """

    def __init__(self) -> None:
        """Start with no records."""
        self.records: list[AuditRecord] = []

    async def record(self, record: AuditRecord) -> None:
        """Append ``record`` to ``records``."""
        self.records.append(record)


_INTERFACE_CHECK: type[AuditSink] = StubAuditSink
"""Type-checker assertion that ``StubAuditSink`` satisfies ``AuditSink``."""
