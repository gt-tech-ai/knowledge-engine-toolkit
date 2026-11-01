"""The ``bedrock_guardrails`` safety check: Bedrock ``ApplyGuardrail``.

A prompt is assessed as guardrail ``INPUT``, an answer as ``OUTPUT``. Each assessment becomes a
flag — ``content.<type>``, ``topic.<name>``, ``pii.<type>``, ``regex.<name>``, ``word.<match>`` —
and an intervention with no itemised assessment is ``guardrail.intervened``. Only
``GUARDRAIL_INTERVENED`` counts as a detection; ``redact`` serves the guardrail's masked output.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, override

from techai_webutils.clients.safety.decision import decide
from techai_webutils.core.interfaces.safety import SafetyCheck, SafetyResult, ScanKind

if TYPE_CHECKING:
    from techai_webutils.clients.safety.aws_client import LazyAwsClient
    from techai_webutils.clients.safety.decision import OnDetect

_INTERVENED = "GUARDRAIL_INTERVENED"
"""The ``ApplyGuardrail`` action that means the guardrail acted on the text."""


def _assessment_flags(assessment: dict[str, Any]) -> list[str]:
    """Return the flags one guardrail assessment raised."""
    content = assessment.get("contentPolicy", {}).get("filters", [])
    topics = assessment.get("topicPolicy", {}).get("topics", [])
    sensitive = assessment.get("sensitiveInformationPolicy", {})
    words = assessment.get("wordPolicy", {})
    return [
        *(f"content.{str(f['type']).lower()}" for f in content),
        *(f"topic.{t['name']}" for t in topics),
        *(f"pii.{str(p['type']).lower()}" for p in sensitive.get("piiEntities", [])),
        *(f"regex.{r['name']}" for r in sensitive.get("regexes", [])),
        *(f"word.{str(w['match']).lower()}" for w in words.get("customWords", [])),
        *(f"word.{str(w['match']).lower()}" for w in words.get("managedWordLists", [])),
    ]


class BedrockGuardrailsSafetyCheck(SafetyCheck):
    """Content and PII safety through a configured Bedrock guardrail."""

    def __init__(
        self,
        client: LazyAwsClient,
        guardrail_id: str,
        guardrail_version: str,
        on_detect: OnDetect,
    ) -> None:
        """Bind the lazily opened ``bedrock-runtime`` client, the guardrail and the policy."""
        self._client = client
        self._guardrail_id = guardrail_id
        self._guardrail_version = guardrail_version
        self._on_detect = on_detect

    @override
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Return the verdict for ``text`` from the guardrail's action and assessments."""
        response = await self._client.call(
            "apply_guardrail",
            guardrailIdentifier=self._guardrail_id,
            guardrailVersion=self._guardrail_version,
            source="INPUT" if kind is ScanKind.PROMPT else "OUTPUT",
            content=[{"text": {"text": text}}],
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
