"""The ``comprehend`` safety check: AWS Comprehend ``DetectPiiEntities``.

Each detected entity becomes a ``pii.<type>`` flag (lower-cased, deduplicated, in
detection order); ``redact`` replaces each entity span with ``[REDACTED:<type>]``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, override

from techai_webutils.clients.safety.decision import decide
from techai_webutils.core.interfaces.safety import SafetyCheck, SafetyResult, ScanKind

if TYPE_CHECKING:
    from techai_webutils.clients.safety.aws_client import LazyAwsClient
    from techai_webutils.clients.safety.decision import OnDetect


def _redact(text: str, entities: list[dict[str, Any]]) -> str:
    """Return ``text`` with every entity span replaced.

    Right to left so offsets stay valid.

    Returns:
        ``text`` with every entity span replaced.

    """
    for entity in sorted(entities, key=lambda e: int(e["BeginOffset"]), reverse=True):
        begin, end = int(entity["BeginOffset"]), int(entity["EndOffset"])
        text = f"{text[:begin]}[REDACTED:{str(entity['Type']).lower()}]{text[end:]}"
    return text


class ComprehendSafetyCheck(SafetyCheck):
    """PII detection through AWS Comprehend."""

    def __init__(
        self, client: LazyAwsClient, on_detect: OnDetect, language: str = "en"
    ) -> None:
        """Bind the lazily opened ``comprehend`` client.

        The policy and the text language.
        """
        self._client = client
        self._on_detect = on_detect
        self._language = language

    @override
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Return the verdict for ``text`` from Comprehend's detected PII entities.

        Returns:
            The verdict for ``text`` from Comprehend's detected PII entities.

        """
        response = await self._client.call(
            "detect_pii_entities", Text=text, LanguageCode=self._language
        )
        entities: list[dict[str, Any]] = list(response.get("Entities", []))
        flags = tuple(dict.fromkeys(f"pii.{str(e['Type']).lower()}" for e in entities))
        return decide(flags, self._on_detect, _redact(text, entities))

    @override
    async def aclose(self) -> None:
        """Release the Comprehend client."""
        await self._client.aclose()
