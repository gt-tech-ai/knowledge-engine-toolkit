# Local stub: the subset of ``grpc_health.v1.health_pb2_grpc`` this package uses.

from collections.abc import Callable

import grpc

# Base class of the generated Health service.
class HealthServicer: ...

# Client of the Health service; its ``Check`` RPC keeps the generated CamelCase name,
# which ruff's pep8-naming rejects in a stub, so only the constructor is declared.
class HealthStub:
    def __init__(self, channel: grpc.aio.Channel) -> None: ...

# The module's one other member this package uses is the generated registrar
# ``add_HealthServicer_to_server``. Its generated name is not snake_case, so ruff's
# pep8-naming rejects it as a ``def`` (N802) or an attribute (N816); typing every other
# attribute as the registrar keeps the call statically typed without naming it here.
def __getattr__(
    name: str,
) -> Callable[[HealthServicer, grpc.Server | grpc.aio.Server], None]: ...
