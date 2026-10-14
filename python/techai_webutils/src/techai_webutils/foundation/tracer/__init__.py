"""Tracer implementations and builder.

Provides OTelTracerProvider, NullTracerProvider, a builder factory for config-driven tracer
selection, and ``inject`` / ``continued`` to carry a trace across a message broker.
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
from techai_webutils.foundation.tracer.propagate import continued, inject
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
    "continued",
    "conversation_links",
    "default_config",
    "extract_conversation",
    "inject",
    "new_tracer",
    "new_tracer_from_config",
    "stamp_conversation",
]
