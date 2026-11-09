"""The ``comprehend`` safety check: AWS Comprehend ``DetectPiiEntities``.

Each detected entity becomes a ``pii.<type>`` flag (lower-cased, deduplicated, in
detection order); ``redact`` replaces each entity span with ``[REDACTED:<type>]``.
"""

from __future__ import annotations

from operator import itemgetter
from typing import TYPE_CHECKING, TypedDict, cast, final, override

from techai_webutils.clients.safety.decision import decide
from techai_webutils.core.interfaces.safety import SafetyCheck, SafetyResult, ScanKind

if TYPE_CHECKING:
    from techai_webutils.clients.safety.aws_client import LazyAwsClient
    from techai_webutils.clients.safety.decision import OnDetect


class _PiiEntity(TypedDict):
    """One entity from ``DetectPiiEntities``."""

    BeginOffset: int
    """Where the span starts."""

    EndOffset: int
    """Where the span ends."""

    Type: str
    """The PII category."""


class _DetectPiiResponse(TypedDict, total=False):
    """The ``DetectPiiEntities`` fields this check reads."""

    Entities: list[_PiiEntity]
    """The entities found."""


def _redact(text: str, entities: list[_PiiEntity]) -> str:
    """Return ``text`` with every entity span replaced.

    Right to left so offsets stay valid.

    Returns:
        ``text`` with every entity span replaced.

    """
    for entity in sorted(entities, key=itemgetter("BeginOffset"), reverse=True):
        begin, end = entity["BeginOffset"], entity["EndOffset"]
        text = f"{text[:begin]}[REDACTED:{str(entity['Type']).lower()}]{text[end:]}"
    return text


@final
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
        response = cast(
            "_DetectPiiResponse",
            await self._client.call(
                "detect_pii_entities", Text=text, LanguageCode=self._language
            ),
        )
        entities = response.get("Entities", [])
        flags = tuple(dict.fromkeys(f"pii.{str(e['Type']).lower()}" for e in entities))
        return decide(flags, self._on_detect, _redact(text, entities))

    @override
    async def aclose(self) -> None:
        """Release the Comprehend client."""
        await self._client.aclose()
