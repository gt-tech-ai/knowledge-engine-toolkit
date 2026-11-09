"""The routed gateway's model catalog and routes, parsed and validated at boot.

A catalog maps stable ``provider/model`` ids to capabilities, priors and one or more
endpoints, each with an ``LlmConfig`` fragment, a region and data-policy facts. Price is
not in the catalog: it comes from the token-economy price table, and ``priced_ids`` makes
every model prove it has a price (``None`` skips the check while a dev price table is
empty). Any defect raises ``AppError(INVALID_INPUT)`` naming the entry, so a bad catalog
stops the boot.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, fields
from typing import TYPE_CHECKING, Any, cast

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.types.llm import LlmConfig, LlmKind
from techai_webutils.core.types.routing import Capabilities, CatalogEndpointRef, Route

if TYPE_CHECKING:
    from types import MappingProxyType

_LLM_FIELDS = frozenset(f.name for f in fields(LlmConfig))
"""``LlmConfig`` fields an endpoint may set."""


def invalid(message: str) -> AppError:
    """Return the coded error every catalog, route and policy defect raises.

    Returns:
        The coded error every catalog, route and policy defect raises.

    """
    return AppError(ErrorCode.INVALID_INPUT, message)


def as_mapping(raw: object, what: str) -> Mapping[str, Any]:
    """Return ``raw`` as a mapping, or raise ``INVALID_INPUT`` naming ``what``.

    Returns:
        ``raw`` as a mapping, or raise ``INVALID_INPUT`` naming ``what``.

    Raises:
        invalid: ``what`` must be a mapping.

    """
    if not isinstance(raw, Mapping):
        msg = f"{what} must be a mapping"
        raise invalid(msg)
    return raw  # pyright: ignore[reportUnknownVariableType]


def as_strings(raw: object, what: str) -> tuple[str, ...]:
    """Return ``raw`` (a list of strings) as a tuple, or raise ``INVALID_INPUT``.

    Returns:
        ``raw`` (a list of strings) as a tuple, or raise ``INVALID_INPUT``.

    Raises:
        invalid: ``what`` must be a list.

    """
    if isinstance(raw, str) or not isinstance(raw, Sequence):
        msg = f"{what} must be a list"
        raise invalid(msg)
    return tuple(str(item) for item in raw)  # pyright: ignore[reportUnknownVariableType, reportUnknownArgumentType]


def _llm_config(raw: object, key: str) -> LlmConfig:
    """Build an endpoint's ``LlmConfig`` from its fragment, stripping ``fallback_model``.

    The routed chain fails over across endpoints itself, so an endpoint never also carries
    the LLM tier's own fallback (the two would stack).

    Returns:
        An endpoint's ``LlmConfig`` from its fragment, stripping ``fallback_model``.

    Raises:
        invalid: Catalog endpoint ``key`` llm has unknown fields ``sorted(unknown)``;
            catalog endpoint ``key`` has unknown llm kind ``spec.get('kind')``.

    """
    spec = as_mapping(raw, f"catalog endpoint {key} llm")
    unknown = set(spec) - _LLM_FIELDS
    if unknown:
        msg = f"catalog endpoint {key} llm has unknown fields {sorted(unknown)}"
        raise invalid(msg)
    try:
        kind = LlmKind(spec.get("kind", ""))
    except ValueError:
        msg = f"catalog endpoint {key} has unknown llm kind {spec.get('kind')!r}"
        raise invalid(msg) from None
    values = cast("dict[str, Any]", {**spec, "kind": kind, "fallback_model": ""})
    return LlmConfig(**values)


@dataclass(frozen=True, slots=True)
class ModelCatalog:
    """The validated catalog.

    Endpoint refs in catalog order plus each one's provider config.
    """

    refs: tuple[CatalogEndpointRef, ...]
    """Every endpoint, model by model, in the order the catalog lists them."""
    llm_configs: MappingProxyType[str, LlmConfig] | Mapping[str, LlmConfig]
    """Endpoint key → the provider config it is built from."""

    def llm_config(self, ref: CatalogEndpointRef) -> LlmConfig:
        """Return the provider config of ``ref``.

        Returns:
            The provider config of ``ref``.

        """
        return self.llm_configs[ref.key]

    @property
    def model_ids(self) -> frozenset[str]:
        """The catalog's model ids."""
        return frozenset(ref.model_id for ref in self.refs)

    @classmethod
    def from_mapping(
        cls, raw: Mapping[str, Any], *, priced_ids: frozenset[str] | None
    ) -> ModelCatalog:
        """Parse and validate ``raw`` (model id → spec).

        A callee raises ``AppError``: ``INVALID_INPUT`` naming the first defective entry.

        Returns:
            The validated catalog.

        Raises:
            invalid: Catalog model ``model_id`` has no price in the price table; catalog
                has no endpoints.

        """
        refs: list[CatalogEndpointRef] = []
        configs: dict[str, LlmConfig] = {}
        for model_id, model_raw in as_mapping(raw, "catalog").items():
            if priced_ids is not None and model_id not in priced_ids:
                msg = f"catalog model {model_id} has no price in the price table"
                raise invalid(msg)
            refs.extend(_parse_model(model_id, as_mapping(model_raw, model_id), configs))
        if not refs:
            msg = "catalog has no endpoints"
            raise invalid(msg)
        return cls(refs=tuple(refs), llm_configs=configs)


def _parse_model(
    model_id: str, spec: Mapping[str, Any], configs: dict[str, LlmConfig]
) -> list[CatalogEndpointRef]:
    """Parse one model's endpoints into refs, recording each endpoint's config.

    Returns:
        One model's endpoints into refs, recording each endpoint's config.

    Raises:
        invalid: Catalog model ``model_id`` is malformed: ``exc``; catalog endpoint
            ``key`` is listed twice; catalog endpoint ``key`` has no llm config; catalog
            model ``model_id`` has no endpoints.

    """
    try:
        capabilities = Capabilities(**as_mapping(spec.get("capabilities"), model_id))
        quality, ttft_ms = float(spec["quality"]), int(spec["ttft_ms"])
    except (KeyError, TypeError, ValueError) as exc:
        msg = f"catalog model {model_id} is malformed: {exc}"
        raise invalid(msg) from None
    refs: list[CatalogEndpointRef] = []
    for endpoint_raw in spec.get("endpoints") or ():
        endpoint = as_mapping(endpoint_raw, f"catalog model {model_id} endpoint")
        key = f"{model_id}@{endpoint.get('name', '')}"
        if key in configs:
            msg = f"catalog endpoint {key} is listed twice"
            raise invalid(msg)
        if "llm" not in endpoint:
            msg = f"catalog endpoint {key} has no llm config"
            raise invalid(msg)
        configs[key] = _llm_config(endpoint["llm"], key)
        refs.append(
            CatalogEndpointRef(
                model_id=model_id,
                endpoint=str(endpoint["name"]),
                capabilities=capabilities,
                quality=quality,
                ttft_ms=ttft_ms,
                region=str(endpoint.get("region", "")),
                zero_retention=bool(endpoint.get("zero_retention", False)),
                leaves_cloud_boundary=bool(endpoint.get("leaves_cloud_boundary", False)),
            )
        )
    if not refs:
        msg = f"catalog model {model_id} has no endpoints"
        raise invalid(msg)
    return refs


def parse_routes(raw: Mapping[str, Any], catalog: ModelCatalog) -> dict[str, Route]:
    """Parse ``raw`` (route name → spec) into routes whose models are all in ``catalog``.

    A callee raises ``AppError``: ``INVALID_INPUT`` for a malformed route or an unknown
    model id.

    Returns:
        The routes by name.

    Raises:
        invalid: Route ``name`` names models not in the catalog: ``sorted(unknown)``.

    """
    routes: dict[str, Route] = {}
    for name, route_raw in as_mapping(raw, "routes").items():
        spec = as_mapping(route_raw, f"route {name}")
        route = Route(
            name=name,
            default_model=str(spec.get("default_model", "")),
            preferred=as_strings(spec.get("preferred", ()), f"route {name} preferred"),
            exemplars=as_strings(spec.get("exemplars", ()), f"route {name} exemplars"),
            threshold=float(spec.get("threshold", 0.0)),
            quality_floor=float(spec.get("quality_floor", 0.0)),
        )
        unknown = {route.default_model, *route.preferred} - catalog.model_ids
        if unknown:
            msg = f"route {name} names models not in the catalog: {sorted(unknown)}"
            raise invalid(msg)
        routes[name] = route
    return routes
