"""``VisionKind`` and ``VisionConfig``: the vision client and its settings."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

DEFAULT_INSTRUCTION = (
    "Transcribe all text on this page image as Markdown, in reading order. Keep "
    "headings, lists and tables. Output only the transcription; output nothing if the "
    "page has no text."
)
"""The instruction sent beside each page image unless ``VisionConfig.instruction``
overrides it."""


class VisionKind(StrEnum):
    """Which vision client to build."""

    STUB = "stub"
    """Reads every page as empty text, with no I/O (the default)."""
    BEDROCK = "bedrock"
    """Sends each page image to a multimodal model through bedrock-runtime
    ``Converse``."""


@dataclass(frozen=True, slots=True)
class VisionConfig:
    """Selects and configures the vision client."""

    kind: VisionKind = VisionKind.STUB
    """The client to build; ``stub`` by default."""
    region: str = "us-east-1"
    """AWS region of the bedrock-runtime endpoint (``bedrock`` kind)."""
    timeout_seconds: float = 120.0
    """Connect and read timeout of each ``Converse`` call (``bedrock`` kind)."""
    max_tokens: int = 8192
    """The reply's token ceiling per page; a reply cut at it is an error, never a
    partial page (``bedrock`` kind)."""
    instruction: str = DEFAULT_INSTRUCTION
    """The text sent beside each page image (``bedrock`` kind)."""
