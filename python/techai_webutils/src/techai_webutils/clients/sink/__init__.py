"""Sinks (``SinkKind``: ``noop``, ``opensearch``, ``bedrock``), their factory, their client-tier decorator and the latency-driven write governor."""

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
