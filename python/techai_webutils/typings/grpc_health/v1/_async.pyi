# Local stub: the asyncio health servicer in ``grpc_health.v1._async``.

from grpc_health.v1 import health_pb2, health_pb2_grpc

# An asyncio implementation of the health-checking servicer.
class HealthServicer(health_pb2_grpc.HealthServicer):
    def __init__(self) -> None: ...
    async def set(
        self, service: str, status: health_pb2.HealthCheckResponse.ServingStatus
    ) -> None: ...
    async def enter_graceful_shutdown(self) -> None: ...
