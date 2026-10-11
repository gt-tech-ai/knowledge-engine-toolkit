# Local stub: the subset of ``grpc_health.v1.health_pb2`` this package uses.
#
# ``grpcio-health-checking`` ships no ``py.typed`` marker, so a local stub package under
# ``typings/`` (pyright's default ``stubPath``) is the typed boundary. A stub package there
# is complete, so it re-declares the message types it needs from the shipped
# ``health_pb2.pyi``.

from typing import ClassVar

from google.protobuf import message
from google.protobuf.internal import enum_type_wrapper

class HealthCheckRequest(message.Message):
    service: str
    def __init__(self, service: str | None = ...) -> None: ...

class HealthCheckResponse(message.Message):
    class ServingStatus(int, metaclass=enum_type_wrapper.EnumTypeWrapper):
        UNKNOWN: ClassVar[HealthCheckResponse.ServingStatus]
        SERVING: ClassVar[HealthCheckResponse.ServingStatus]
        NOT_SERVING: ClassVar[HealthCheckResponse.ServingStatus]
        SERVICE_UNKNOWN: ClassVar[HealthCheckResponse.ServingStatus]

    UNKNOWN: HealthCheckResponse.ServingStatus
    SERVING: HealthCheckResponse.ServingStatus
    NOT_SERVING: HealthCheckResponse.ServingStatus
    SERVICE_UNKNOWN: HealthCheckResponse.ServingStatus
    status: HealthCheckResponse.ServingStatus
    def __init__(
        self, status: HealthCheckResponse.ServingStatus | str | None = ...
    ) -> None: ...
