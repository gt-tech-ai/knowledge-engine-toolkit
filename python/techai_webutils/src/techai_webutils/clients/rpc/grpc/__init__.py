"""gRPC client backend: the channel factory and its client-tier interceptors.

The gRPC server runtime and its interceptors live in ``controllers.grpc`` (the controller tier).
"""

from techai_webutils.clients.rpc.grpc.client import ChannelConfig, create_channel
from techai_webutils.clients.rpc.grpc.interceptors import client_interceptors

__all__ = ["ChannelConfig", "client_interceptors", "create_channel"]
