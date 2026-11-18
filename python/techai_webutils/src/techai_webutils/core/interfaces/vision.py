"""Vision-model contract: read the text off a rendered page image."""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class VisionClient(Protocol):
    """A multimodal model that transcribes the text of one page image.

    A parser uses it to read pages that carry no text layer (scans). The consumer injects
    the backend, so the model, its region and its credentials stay outside the parser.
    """

    async def describe_page(self, png: bytes, *, model_id: str) -> str:
        """Return the text printed on the page image ``png``.

        Args:
            png: The rendered page, PNG-encoded.
            model_id: The vision model to ask.

        Returns:
            The page's text, in reading order; empty when the page has no text.

        """
        ...
