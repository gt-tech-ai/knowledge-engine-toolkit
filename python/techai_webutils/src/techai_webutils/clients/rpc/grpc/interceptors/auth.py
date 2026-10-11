"""gRPC server interceptor for extracting authentication claims from metadata.

Mirrors Go's ``go/clients/transport/connect/interceptors/auth.go``.

A gateway sets identity headers after validating the caller's token. This
interceptor extracts those headers from gRPC metadata — under the names in its
``HeaderClaimMapping`` — and makes them available downstream via ``contextvars``.
"""

from __future__ import annotations

import contextvars
from dataclasses import dataclass, field, fields
from typing import TYPE_CHECKING

import grpc

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


@dataclass(frozen=True)
class AuthClaims:
    """Authentication claims extracted from request metadata.

    The Python counterpart of Go's ``AuthClaims``, narrowed to the claims this interceptor reads:
    ``user_id`` ↔ ``Sub``, ``tenant_id`` ↔ ``TenantID``, ``roles`` ↔ ``Roles`` (Go also carries
    ``Email``, ``Name``, ``NickName`` and ``Synthetic``).
    """

    user_id: str = ""
    """The authenticated caller's subject id."""
    tenant_id: str = ""
    """The tenant (organization) the caller authenticates under."""
    roles: frozenset[str] = field(default_factory=frozenset)
    """The caller's roles as the gateway forwards them (comma-separated in the metadata)."""


@dataclass(frozen=True, slots=True)
class HeaderClaimMapping:
    """The gRPC metadata keys each claim is read from, so a consumer matches its gateway.

    There are no defaults: header names are the consumer's gateway contract (Go's ``HeaderMap``
    ``Sub``/``Tenant``/``Roles``). gRPC metadata keys are always lowercase, so each name is trimmed and
    lowercased at construction (``"X-User-Sub"`` matches like Go's case-insensitive header lookup); an
    empty or blank name raises ``ValueError``.
    """

    user_id: str
    """Key carrying the subject id; its absence means the request is unauthenticated."""
    tenant_id: str
    """Key carrying the tenant (organization) id."""
    roles: str
    """Key carrying the comma-separated roles."""

    def __post_init__(self) -> None:
        """Normalise every key to its lowercase, trimmed metadata form; reject an empty or blank one."""
        for claim in fields(self):
            raw = getattr(self, claim.name)
            key = raw.strip().lower()
            if not key:
                msg = f"HeaderClaimMapping.{claim.name} must name a metadata key, got {raw!r}"
                raise ValueError(msg)
            object.__setattr__(self, claim.name, key)


_auth_claims_var: contextvars.ContextVar[AuthClaims | None] = contextvars.ContextVar(
    "auth_claims",
    default=None,
)


def set_auth_claims(claims: AuthClaims) -> contextvars.Token[AuthClaims | None]:
    """Store auth claims in the current context."""
    return _auth_claims_var.set(claims)


def get_auth_claims() -> AuthClaims | None:
    """Retrieve auth claims from the current context."""
    return _auth_claims_var.get()


def _split_roles(raw: str) -> frozenset[str]:
    """Parse comma-separated roles, discarding empty segments."""
    if not raw:
        return frozenset()
    return frozenset(r.strip() for r in raw.split(",") if r.strip())


class AuthServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Async server interceptor that extracts auth claims from gRPC metadata.

    Only processes authenticated requests (when the mapping's ``user_id`` key is present).
    Does NOT validate tokens — that's delegated to the gateway (or, for internal
    service-to-service calls, the service-auth interceptor).
    """

    def __init__(self, headers: HeaderClaimMapping) -> None:
        """Read claims from the metadata keys in ``headers``."""
        self._headers = headers

    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Callable[
            [grpc.HandlerCallDetails],
            Awaitable[grpc.RpcMethodHandler[TRequest, TResponse] | None],
        ],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Extract auth headers and store as AuthClaims in contextvars."""
        # The gRPC Python API for server interceptors passes metadata via
        # handler_call_details.invocation_metadata
        metadata = dict(handler_call_details.invocation_metadata or [])
        keys = self._headers
        user_id = str(metadata.get(keys.user_id, ""))

        if user_id:
            claims = AuthClaims(
                user_id=user_id,
                tenant_id=str(metadata.get(keys.tenant_id, "")),
                roles=_split_roles(str(metadata.get(keys.roles, ""))),
            )
            set_auth_claims(claims)

        return await continuation(handler_call_details)
