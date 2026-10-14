"""The gRPC controller transport (mirrors Go ``transport/grpc``).

The ``grpc.aio`` server, its status mapping, and the interceptors that run every RPC through the
controller tier.
"""

from techai_webutils.controllers.grpc.errors import (
    abort_with_error,
    safe_status,
    to_grpc_status,
)
from techai_webutils.controllers.grpc.interceptors import Edge, server_interceptors
from techai_webutils.controllers.grpc.server import (
    GracefulServer,
    ServerConfig,
    create_server,
)

__all__ = [
    "Edge",
    "GracefulServer",
    "ServerConfig",
    "abort_with_error",
    "create_server",
    "safe_status",
    "server_interceptors",
    "to_grpc_status",
]
