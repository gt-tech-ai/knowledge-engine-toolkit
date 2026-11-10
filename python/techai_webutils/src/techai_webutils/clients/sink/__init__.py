"""Sinks, their factory, client-tier decorator and write governor.

The kinds (``SinkKind``) are ``noop``, ``opensearch`` and ``bedrock``; the governor is
latency-driven.
"""

from techai_webutils.clients.sink.bedrock import BedrockLayout, BedrockSinkConfig
from techai_webutils.clients.sink.builder import SinkConfig, SinkKind, sink_from_config
from techai_webutils.clients.sink.decorated import decorate
from techai_webutils.clients.sink.governor import GovernorConfig, SinkWriteGovernor

__all__ = [
    "BedrockLayout",
    "BedrockSinkConfig",
    "GovernorConfig",
    "SinkConfig",
    "SinkKind",
    "SinkWriteGovernor",
    "decorate",
    "sink_from_config",
]
