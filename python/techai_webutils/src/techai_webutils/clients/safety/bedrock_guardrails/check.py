"""The ``bedrock_guardrails`` safety check: Bedrock ``ApplyGuardrail``.

A prompt is assessed as guardrail ``INPUT``, an answer as ``OUTPUT``. Each assessment
becomes a flag — ``content.<type>``, ``topic.<name>``, ``pii.<type>``, ``regex.<name>``,
``word.<match>`` — and an intervention with no itemised assessment is
``guardrail.intervened``. Only ``GUARDRAIL_INTERVENED`` counts as a detection; ``redact``
serves the guardrail's masked output.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, TypedDict, cast, final, override

from techai_webutils.clients.safety.decision import decide
from techai_webutils.core.interfaces.safety import SafetyCheck, SafetyResult, ScanKind

if TYPE_CHECKING:
    from techai_webutils.clients.safety.aws_client import LazyAwsClient
    from techai_webutils.clients.safety.decision import OnDetect

_INTERVENED = "GUARDRAIL_INTERVENED"
"""The ``ApplyGuardrail`` action that means the guardrail acted on the text."""


class _Filter(TypedDict):
    """One content filter a guardrail applied."""

    type: str
    """The filter category."""


class _Topic(TypedDict):
    """One denied topic a guardrail matched."""

    name: str
    """The topic name."""


class _Pii(TypedDict):
    """One PII entity a guardrail found."""

    type: str
    """The PII category."""


class _Regex(TypedDict):
    """One regex a guardrail matched."""

    name: str
    """The configured regex name."""


class _Word(TypedDict):
    """One word a guardrail matched."""

    match: str
    """The matched text."""


class _ContentPolicy(TypedDict, total=False):
    """The content-filter part of an assessment."""

    filters: list[_Filter]
    """The filters that fired."""


class _TopicPolicy(TypedDict, total=False):
    """The denied-topic part of an assessment."""

    topics: list[_Topic]
    """The topics that fired."""


class _SensitivePolicy(TypedDict, total=False):
    """The sensitive-information part of an assessment."""

    piiEntities: list[_Pii]
    """The PII entities found."""

    regexes: list[_Regex]
    """The regexes matched."""


class _WordPolicy(TypedDict, total=False):
    """The word-filter part of an assessment."""

    customWords: list[_Word]
    """Matches from custom lists."""

    managedWordLists: list[_Word]
    """Matches from managed lists."""


class _Assessment(TypedDict, total=False):
    """One ``ApplyGuardrail`` assessment."""

    contentPolicy: _ContentPolicy
    """Content filters."""

    topicPolicy: _TopicPolicy
    """Denied topics."""

    sensitiveInformationPolicy: _SensitivePolicy
    """Sensitive information."""

    wordPolicy: _WordPolicy
    """Word filters."""


class _Output(TypedDict, total=False):
    """One masked output segment."""

    text: str
    """The masked text."""


class _ApplyGuardrailResponse(TypedDict, total=False):
    """The ``ApplyGuardrail`` response fields this check reads."""

    action: str
    """What the guardrail did."""

    assessments: list[_Assessment]
    """What it assessed."""

    outputs: list[_Output]
    """The masked text."""


def _assessment_flags(assessment: _Assessment) -> list[str]:
    """Return the flags one guardrail assessment raised.

    Args:
        assessment: One guardrail assessment.

    Returns:
        The flags one guardrail assessment raised.

    """
    content = assessment.get("contentPolicy", _ContentPolicy()).get("filters", [])
    topics = assessment.get("topicPolicy", _TopicPolicy()).get("topics", [])
    sensitive = assessment.get("sensitiveInformationPolicy", _SensitivePolicy())
    words = assessment.get("wordPolicy", _WordPolicy())
    return [
        *(f"content.{str(f['type']).lower()}" for f in content),
        *(f"topic.{t['name']}" for t in topics),
        *(f"pii.{str(p['type']).lower()}" for p in sensitive.get("piiEntities", [])),
        *(f"regex.{r['name']}" for r in sensitive.get("regexes", [])),
        *(f"word.{str(w['match']).lower()}" for w in words.get("customWords", [])),
        *(f"word.{str(w['match']).lower()}" for w in words.get("managedWordLists", [])),
    ]


@final
class BedrockGuardrailsSafetyCheck(SafetyCheck):
    """Content and PII safety through a configured Bedrock guardrail."""

    def __init__(
        self,
        client: LazyAwsClient,
        guardrail_id: str,
        guardrail_version: str,
        on_detect: OnDetect,
    ) -> None:
        """Bind the lazily opened ``bedrock-runtime`` client and the guardrail.

        The policy is bound too.

        Args:
            client: The lazily opened ``bedrock-runtime`` client.
            guardrail_id: The guardrail.
            guardrail_version: Its version.
            on_detect: What a check does when it detects something: flag, redact or block.

        """
        self._client = client
        self._guardrail_id = guardrail_id
        self._guardrail_version = guardrail_version
        self._on_detect = on_detect

    @override
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Return the verdict for ``text`` from the guardrail's action and assessments.

        Args:
            text: The text to scan.
            kind: Whether ``text`` is a prompt or a model's answer.

        Returns:
            The verdict for ``text`` from the guardrail's action and assessments.

        """
        response = cast(
            "_ApplyGuardrailResponse",
            await self._client.call(
                "apply_guardrail",
                guardrailIdentifier=self._guardrail_id,
                guardrailVersion=self._guardrail_version,
                source="INPUT" if kind is ScanKind.PROMPT else "OUTPUT",
                content=[{"text": {"text": text}}],
            ),
        )
        if response.get("action") != _INTERVENED:
            return decide((), self._on_detect, text)
        raised = [
            f for a in response.get("assessments", []) for f in _assessment_flags(a)
        ]
        flags = tuple(dict.fromkeys(raised)) or ("guardrail.intervened",)
        outputs = response.get("outputs", [])
        masked = "".join(str(o.get("text", "")) for o in outputs) if outputs else text
        return decide(flags, self._on_detect, masked)

    @override
    async def aclose(self) -> None:
        """Release the Bedrock runtime client."""
        await self._client.aclose()
