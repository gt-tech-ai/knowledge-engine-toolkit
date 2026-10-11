"""RPC client tier — transport backends for service-to-service calls.

Mirrors Go's ``go/clients/rpc``: the gRPC backend lives in ``rpc/grpc`` (channel
factory, graceful server, and the interceptor stack). Selecting a different RPC
transport would add a sibling backend here.
"""

from techai_webutils.clients.rpc.builder import (
    RpcConfig,
    RpcKind,
    new_rpc_channel_from_config,
)

__all__ = ["RpcConfig", "RpcKind", "new_rpc_channel_from_config"]
