"""Unit tests for ``log_context``.

It adds correlation fields every log line inside a block carries.
"""

from __future__ import annotations

import asyncio
import json
from io import StringIO

from techai_webutils.foundation.logger import log_context
from techai_webutils.foundation.logger.logger import (
    StructlogLogger,
    configure_logging,
    get_logger,
)


def _lines(out: StringIO) -> list[dict[str, object]]:
    """Return the JSON lines written to ``out``.

    Args:
        out: The captured log output.

    Returns:
        The JSON lines written to ``out``.

    """
    return [json.loads(line) for line in out.getvalue().splitlines() if line]


def test_log_context_binds_fields_per_task_and_only_inside_the_block() -> None:
    """Test that bound fields reach every line inside the block, per task.

    Not after it.

    **Why this test is important:**
      - A stage job binds one document's ids for the whole handling; a line logged by a
        handler or a client decorator it calls must carry them without hand-formatting,
        and two documents handled at once must never swap ids (the fields are per task).

    **What it tests:**
      - Two concurrent tasks each log under their own ``document_id``; a nested block
        overrides a field and restores it; a line after the block carries none.
    """
    out = StringIO()
    _ = configure_logging("INFO", stream=out)
    logger = StructlogLogger(get_logger("log-context-test"))

    async def handle(doc: str) -> None:
        """Log inside, nested in and after a document's context.

        Args:
            doc: The document.

        """
        with log_context(document_id=doc, stage="parse"):
            await asyncio.sleep(0)
            logger.info("inside", step="first")
            with log_context(stage="enrich"):
                logger.info("nested")
            logger.info("restored")

    async def both() -> None:
        """Handle two documents concurrently."""
        _ = await asyncio.gather(handle("doc-a"), handle("doc-b"))

    asyncio.run(both())
    logger.info("after")

    lines = _lines(out)
    for doc in ("doc-a", "doc-b"):
        mine = [ln for ln in lines if ln.get("document_id") == doc]
        assert [(ln["message"], ln["stage"]) for ln in mine] == [
            ("inside", "parse"),
            ("nested", "enrich"),
            ("restored", "parse"),
        ]
    after = next(ln for ln in lines if ln["message"] == "after")
    assert "document_id" not in after
    assert "stage" not in after
