"""Stage 1 of a routed call: the deterministic filter.

Every endpoint is checked, in order, for capability (window, streaming, tools), data
policy (region, zero retention, off-cloud), the step's deny list and the step's allow
list; each drop records a reason (``capability``, ``data_policy``, ``denied``,
``not_allowed``). An explicit choice — the end user's (``user`` mode, or ``overlord`` with
``user_override``) or else a pin — narrows the candidates to that model *before* the
rules, so neither can bypass them. An empty
result is never a silent default: ``FORBIDDEN`` when a policy rule removed a candidate,
    else
``INVALID_INPUT`` (no model fits the request).
"""

from __future__ import annotations

from dataclasses import dataclass
from types import MappingProxyType
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.types.routing import (
        CatalogEndpointRef,
        RouteRequirements,
        RoutingPolicy,
    )

_POLICY_REASONS = frozenset({"data_policy", "denied", "not_allowed"})
"""Rejection reasons that make an empty result a policy denial (``FORBIDDEN``)."""


@dataclass(frozen=True, slots=True)
class FilterResult:
    """What survived the filter, and why the rest did not."""

    kept: tuple[CatalogEndpointRef, ...]
    """Surviving endpoints, in catalog order."""
    rejections: Mapping[str, str]
    """Endpoint key → why it was dropped."""
    direct_model: str | None
    """The model an explicit choice selected (the plan stage is skipped), if any."""
    decider: str
    """``user`` or ``admin_pin`` for a direct choice, else ``local``."""


def _fits(ref: CatalogEndpointRef, req: RouteRequirements) -> bool:
    """Return whether ``ref``'s model can technically serve ``req``.

    Args:
        ref: The endpoint.
        req: What the call needs.

    Returns:
        Whether ``ref``'s model can technically serve ``req``.

    """
    caps = ref.capabilities
    return (
        caps.context_tokens >= req.prompt_tokens + req.max_tokens
        and (caps.streaming or not req.streaming)
        and (caps.tools or not req.tools)
    )


def _data_policy_ok(ref: CatalogEndpointRef, policy: RoutingPolicy) -> bool:
    """Return whether ``ref`` satisfies residency, retention and the off-cloud rule.

    Args:
        ref: The endpoint.
        policy: The tenant's routing policy.

    Returns:
        Whether ``ref`` satisfies residency, retention and the off-cloud rule.

    """
    return (
        (not policy.region or ref.region == policy.region)
        and (ref.zero_retention or not policy.zero_retention)
        and (policy.allow_off_cloud or not ref.leaves_cloud_boundary)
    )


def _rejection(
    ref: CatalogEndpointRef, policy: RoutingPolicy, step: str, req: RouteRequirements
) -> str | None:
    """Return why ``ref`` may not serve this request, or ``None`` when it may.

    Args:
        ref: The endpoint.
        policy: The tenant's routing policy.
        step: The pipeline step.
        req: What the call needs.

    Returns:
        Why ``ref`` may not serve this request, or ``None`` when it may.

    """
    if not _fits(ref, req):
        return "capability"
    if not _data_policy_ok(ref, policy):
        return "data_policy"
    if ref.model_id in policy.denied(step):
        return "denied"
    allowed = policy.allowed(step)
    if allowed and ref.model_id not in allowed:
        return "not_allowed"
    return None


def _direct_choice(policy: RoutingPolicy, ctx: RouteContext) -> tuple[str | None, str]:
    """Return the explicitly chosen model and who chose it, or ``(None, "local")``.

    Args:
        policy: The tenant's routing policy.
        ctx: The call's route context.

    Returns:
        The explicitly chosen model and who chose it, or ``(None, "local")``.

    """
    honours_user = policy.mode == "user" or policy.user_override
    if ctx.model_preference and honours_user:
        return ctx.model_preference, "user"
    pinned = policy.pin(ctx.step)
    if pinned:
        return pinned, "admin_pin"
    return None, "local"


def filter_candidates(
    refs: tuple[CatalogEndpointRef, ...],
    policy: RoutingPolicy,
    ctx: RouteContext,
    req: RouteRequirements,
) -> FilterResult:
    """Return the endpoints that may serve ``ctx``'s request under ``policy``.

    Args:
        refs: Every catalog endpoint.
        policy: The tenant's routing policy.
        ctx: The call's route context.
        req: What the call needs.

    Returns:
        The endpoints that may serve ``ctx``'s request under ``policy``.

    Raises:
        AppError: ``FORBIDDEN`` when policy leaves nothing; ``INVALID_INPUT`` when no
            model fits the request or an explicit choice names a model the catalog lacks.

    """
    direct, decider = _direct_choice(policy, ctx)
    pool = refs
    if direct is not None:
        pool = tuple(ref for ref in refs if ref.model_id == direct)
        if not pool:
            msg = f"model {direct!r} is not in the catalog"
            raise AppError(ErrorCode.INVALID_INPUT, msg)
    rejections: dict[str, str] = {}
    kept: list[CatalogEndpointRef] = []
    for ref in pool:
        reason = _rejection(ref, policy, ctx.step, req)
        if reason is None:
            kept.append(ref)
        else:
            rejections[ref.key] = reason
    if not kept:
        if _POLICY_REASONS & set(rejections.values()):
            raise AppError(ErrorCode.FORBIDDEN, "routing policy allows no endpoint")
        raise AppError(ErrorCode.INVALID_INPUT, "no model fits the request")
    return FilterResult(
        kept=tuple(kept),
        rejections=MappingProxyType(rejections),
        direct_model=direct,
        decider=decider,
    )
