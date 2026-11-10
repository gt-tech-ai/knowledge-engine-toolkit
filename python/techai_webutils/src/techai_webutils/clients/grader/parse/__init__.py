"""The ``parse`` grader: parse quality against golden blocks.

It grades tables, reading order and headings.
"""

from techai_webutils.clients.grader.parse.grader import (
    EXPECTED_BLOCKS_KEY,
    ParseAspect,
    ParseGrader,
)

__all__ = ["EXPECTED_BLOCKS_KEY", "ParseAspect", "ParseGrader"]
