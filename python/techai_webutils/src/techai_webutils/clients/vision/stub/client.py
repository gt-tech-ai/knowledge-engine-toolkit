"""``StubVisionClient``: the zero-infrastructure default vision client."""

from __future__ import annotations

from typing import final, override

from techai_webutils.core.interfaces.vision import VisionClient


@final
class StubVisionClient(VisionClient):
    """Reads every page as empty text, so a graph boots with no model or network."""

    @override
    async def describe_page(self, png: bytes, *, model_id: str) -> str:
        """Return empty text.

        Args:
            png: Ignored.
            model_id: Ignored.

        Returns:
            ``""``.

        """
        return ""
