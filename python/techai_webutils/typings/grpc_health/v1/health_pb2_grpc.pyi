# Local stub: the subset of ``grpc_health.v1.health_pb2_grpc`` this package uses.

import grpc

# Base class of the generated Health service.
class HealthServicer: ...

def add_HealthServicer_to_server(
    servicer: HealthServicer, server: grpc.Server | grpc.aio.Server
) -> None: ...
