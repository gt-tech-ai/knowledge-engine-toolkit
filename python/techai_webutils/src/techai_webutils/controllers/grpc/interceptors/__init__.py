"""gRPC server interceptors: the controller tier laid out over ``grpc.aio``.

``server_interceptors`` lays the tier out with its gRPC-native observability; the edge
interceptors (service auth, claims, validation) run around it.
"""

from techai_webutils.controllers.grpc.interceptors.auth import (
    AuthClaims,
    AuthServerInterceptor,
    HeaderClaimMapping,
    get_auth_claims,
    set_auth_claims,
)
from techai_webutils.controllers.grpc.interceptors.edge import (
    ServiceAuthServerInterceptor,
    ValidatingServerInterceptor,
    service_auth,
)
from techai_webutils.controllers.grpc.interceptors.observe import (
    LoggingServerInterceptor,
    MetricsServerInterceptor,
    TracingServerInterceptor,
)
from techai_webutils.controllers.grpc.interceptors.server import Edge, server_interceptors

__all__ = [
    "AuthClaims",
    "AuthServerInterceptor",
    "Edge",
    "HeaderClaimMapping",
    "LoggingServerInterceptor",
    "MetricsServerInterceptor",
    "ServiceAuthServerInterceptor",
    "TracingServerInterceptor",
    "ValidatingServerInterceptor",
    "get_auth_claims",
    "server_interceptors",
    "service_auth",
    "set_auth_claims",
]
