"""The ``bedrock_guardrails`` safety check: Bedrock ApplyGuardrail (lazy aiobotocore)."""

from techai_webutils.clients.safety.bedrock_guardrails.check import (
    BedrockGuardrailsSafetyCheck,
)

__all__ = ["BedrockGuardrailsSafetyCheck"]
