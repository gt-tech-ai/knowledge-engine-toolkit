"""Tracer implementations and builder.

Provides OTelTracerProvider, NullTracerProvider, and a builder factory
for config-driven tracer selection.
"""

from techai_webutils.foundation.tracer.builder import (
    TracerConfig,
    TracerKind,
    default_config,
    new_tracer_from_config,
)
from techai_webutils.foundation.tracer.conversation import (
    ConversationContext,
    conversation_links,
    extract_conversation,
    stamp_conversation,
)
from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider
from techai_webutils.foundation.tracer.tracer import (
    OTelTracerProvider,
    configure_tracer,
    new_tracer,
)

__all__ = [
    "ConversationContext",
    "NullTracerProvider",
    "OTelTracerProvider",
    "TracerConfig",
    "TracerKind",
    "configure_tracer",
    "conversation_links",
    "default_config",
    "extract_conversation",
    "new_tracer",
    "new_tracer_from_config",
    "stamp_conversation",
]
