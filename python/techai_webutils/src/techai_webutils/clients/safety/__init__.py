"""PII and content safety: a config-selected ``SafetyCheck``.

The kinds are noop, regex, comprehend and guardrails.
"""

from techai_webutils.clients.safety.builder import (
    SafetyConfig,
    SafetyKind,
    new_safety_check_from_config,
)
from techai_webutils.clients.safety.decision import OnDetect

__all__ = ["OnDetect", "SafetyConfig", "SafetyKind", "new_safety_check_from_config"]
