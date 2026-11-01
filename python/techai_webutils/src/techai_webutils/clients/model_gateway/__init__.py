"""Model gateway: a config-selected ``ModelGateway`` (``passthrough`` or ``routed``)."""

from techai_webutils.clients.model_gateway.builder import (
    GatewayConfig,
    GatewayKind,
    RoutedConfig,
    new_model_gateway_from_config,
)

__all__ = [
    "GatewayConfig",
    "GatewayKind",
    "RoutedConfig",
    "new_model_gateway_from_config",
]
