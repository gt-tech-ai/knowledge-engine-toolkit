"""Typed config models for the routed gateway: the catalog, the routes and the stages.

``RoutedConfig`` holds its catalog, routes and stage blocks as plain mappings, which the
gateway parses at boot. A consumer that loads them from layered YAML into a settings class
uses these models as the field types instead, so a typo or a wrongly typed value fails the
settings load with pydantic's ``ValidationError`` (naming the offending path) rather than
reaching the gateway as an untyped ``dict``:

- ``CatalogModelSetting`` is one catalog model (``catalog: dict[str,
  CatalogModelSetting]``), with its ``CapabilitiesSetting`` and ``CatalogEndpointSetting``
  list; each endpoint's ``llm`` is an ``EndpointLlmSetting`` whose unset fields a consumer
  may fill from its own LLM defaults (``with_llm_defaults``).
- ``RouteSetting`` is one route (``routes: dict[str, RouteSetting]``).
- ``RoutedStagesSetting`` is every other ``RoutedConfig`` field (the default route, the
  stage blocks, the objective and the failover bounds); ``routed_config`` assembles the
  three into a ``RoutedConfig``.

Every model refuses unknown keys and is frozen. The stage blocks (``planner``,
``classifier``, ``ranker``, ``policy_store``, ``sink``, ``shadow``) stay mappings: each
stage's ``Kind`` factory validates its own block when the gateway boots.

The module does not defer its annotations: pydantic reads them at class creation.
"""

from typing import TYPE_CHECKING, ClassVar, cast

from pydantic import BaseModel, ConfigDict, Field

from techai_webutils.clients.model_gateway.routed.config import RoutedConfig
from techai_webutils.core.types.llm import LlmKind
from techai_webutils.core.types.routing import Route

if TYPE_CHECKING:
    from collections.abc import Callable

_STRICT: ConfigDict = ConfigDict(
    extra="forbid", frozen=True, use_attribute_docstrings=True
)
"""Unknown keys are refused, a loaded value is immutable, and each attribute docstring is
the field's schema description; an aliased field validates by its config key or its
name."""


class CapabilitiesSetting(BaseModel):
    """What a catalog model can do (mirrors ``core.types.routing.Capabilities``)."""

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    context_tokens: int = Field(gt=0)
    """Prompt plus output tokens the model accepts."""
    max_output: int = Field(gt=0)
    """Most tokens the model generates in one call."""
    streaming: bool = True
    """Whether the model streams."""
    tools: bool = False
    """Whether the model accepts tool definitions."""
    json_mode: bool = Field(default=False, alias="json")
    """Whether the model has a JSON output mode (``json`` in config; renamed here because
    ``BaseModel.json`` is a method)."""


class EndpointLlmSetting(BaseModel):
    """An endpoint's provider config: the ``LlmConfig`` fields it sets.

    Every one optional.

    A field left unset inherits the consumer's default
    (``CatalogModelSetting.with_llm_defaults``), else ``LlmConfig``'s. ``fallback_model``
    is accepted but the catalog strips it, since the routed chain fails over across
    endpoints itself.
    """

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    kind: LlmKind | None = None
    """The provider kind (``stub``, ``ollama``, ``bedrock``)."""
    region: str | None = None
    """The AWS region of a Bedrock endpoint."""
    endpoint: str | None = None
    """A Bedrock endpoint override."""
    model: str | None = None
    """The provider's model id (a Bedrock model or inference-profile id, or an Ollama
    model)."""
    fallback_model: str | None = None
    """Ignored: the catalog strips it."""
    host: str | None = None
    """The Ollama server URL."""
    timeout_seconds: float | None = Field(default=None, gt=0)
    """The per-request HTTP timeout of an Ollama endpoint, in seconds."""

    def fragment(self) -> dict[str, object]:
        """Return the fields this endpoint sets, as the catalog's ``llm`` mapping.

        Returns:
            The fields this endpoint sets, as the catalog's ``llm`` mapping.

        """
        return self.model_dump(mode="json", exclude_unset=True)


class CatalogEndpointSetting(BaseModel):
    """One servable endpoint of a catalog model."""

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    name: str = Field(min_length=1)
    """The endpoint's name, unique within its model."""
    llm: EndpointLlmSetting
    """The provider config the endpoint is built from."""
    region: str = ""
    """Where the endpoint runs (data residency)."""
    zero_retention: bool = False
    """Whether the endpoint retains no prompt or completion data."""
    leaves_cloud_boundary: bool = False
    """Whether calls leave the deployment's own cloud account."""


class CatalogModelSetting(BaseModel):
    """One catalog model: its capabilities, priors and endpoints.

    (keyed by its catalog id).
    """

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    capabilities: CapabilitiesSetting
    """What the model can do."""
    quality: float = Field(ge=0, le=1)
    """The model's quality prior in ``[0, 1]``."""
    ttft_ms: int = Field(ge=0)
    """The model's time-to-first-token prior, in milliseconds."""
    endpoints: tuple[CatalogEndpointSetting, ...] = Field(min_length=1)
    """The endpoints that serve the model, in failover order."""

    def served_models(self) -> tuple[str, ...]:
        """Return the provider model id each endpoint sets (``llm.model``).

        In endpoint order.

        A provider reports usage under these ids, not the catalog id, so a price keyed by
        the catalog id is also indexed under them.

        Returns:
            The provider model id each endpoint sets (``llm.model``).

        """
        return tuple(e.llm.model for e in self.endpoints if e.llm.model is not None)

    def with_llm_defaults(self, defaults: EndpointLlmSetting) -> "CatalogModelSetting":
        """Return this model with each endpoint's unset ``llm`` fields taken.

        From ``defaults``.

        A consumer passes its own LLM tier's connection fields (kind, region, host), so
        the catalog names only models and one config change moves every endpoint with the
        rest of its LLM tier. A field an endpoint sets wins.

        Args:
            defaults: The consumer's LLM connection fields.

        Returns:
            This model with each endpoint's unset ``llm`` fields taken.

        """
        base = defaults.fragment()
        endpoints = tuple(
            e.model_copy(
                update={"llm": EndpointLlmSetting.model_validate(base | e.llm.fragment())}
            )
            for e in self.endpoints
        )
        return self.model_copy(update={"endpoints": endpoints})

    def as_mapping(self) -> dict[str, object]:
        """Return the model as the raw mapping ``ModelCatalog.from_mapping`` parses.

        Returns:
            The model as the raw mapping ``ModelCatalog.from_mapping`` parses.

        """
        raw = self.model_dump(mode="json", by_alias=True, exclude={"endpoints"})
        raw["endpoints"] = [
            e.model_dump(mode="json", exclude={"llm"}) | {"llm": e.llm.fragment()}
            for e in self.endpoints
        ]
        return raw


class RouteSetting(BaseModel):
    """One route (keyed by its name): the models that serve a task class."""

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    default_model: str = ""
    """The catalog id that serves the route when nothing else decides."""
    preferred: tuple[str, ...] = ()
    """Catalog ids in preference order, after ``default_model``."""
    exemplars: tuple[str, ...] = ()
    """Example utterances a semantic classifier compares a query with."""
    threshold: float = Field(default=0.0, ge=0, le=1)
    """The classifier confidence below which the step's default route is used."""
    quality_floor: float = Field(default=0.0, ge=0, le=1)
    """The lowest quality prior a candidate may have for this route."""

    def route(self, name: str) -> Route:
        """Return the route this entry describes under ``name``.

        (the key it sits under).

        Args:
            name: The route's key.

        Returns:
            The route this entry describes under ``name``.

        """
        return Route(
            name=name,
            default_model=self.default_model,
            preferred=self.preferred,
            exemplars=self.exemplars,
            threshold=self.threshold,
            quality_floor=self.quality_floor,
        )


class RoutedStagesSetting(BaseModel):
    """Every ``RoutedConfig`` field but the catalog and the routes, with its default."""

    model_config: ClassVar[ConfigDict] = _STRICT
    """Strict validation, shared by every setting here (``_STRICT``)."""

    default_route: str = ""
    """The route a step with no route of its own uses; empty makes that an error."""
    planner: dict[str, object] = Field(default_factory=dict)
    """The planner's block (``kind``: ``local`` or ``overlord``)."""
    classifier: dict[str, object] = Field(default_factory=dict)
    """The classifier's block (``kind``: ``stub`` or ``embedding``)."""
    ranker: dict[str, object] = Field(default_factory=dict)
    """The ranker's block (``kind``: ``static_order`` or ``cost_aware``)."""
    policy_store: dict[str, object] = Field(default_factory=dict)
    """The policy store's block (``kind``: ``config``; ``policies``, ``workspaces``)."""
    default_slo: str = "quality"
    """The ranking objective when neither the request nor the tenant policy names one."""
    max_attempts: int = Field(default=2, ge=1)
    """Most endpoints one call tries (open-breaker skips don't count)."""
    breaker_failures: int = Field(default=5, ge=1)
    """Consecutive transient failures that open an endpoint's breaker."""
    breaker_recovery_s: float = Field(default=30.0, gt=0)
    """Seconds an open breaker waits before letting a trial call through."""
    sink: dict[str, object] = Field(default_factory=dict)
    """The route-decision sink's block (``kind``: ``noop`` or ``analytics``)."""
    shadow: dict[str, object] | None = None
    """A shadow ``classifier`` / ``ranker`` / ``planner`` planned beside the live one;
    ``None`` runs no shadow."""

    def routed_config(
        self,
        catalog: dict[str, CatalogModelSetting],
        routes: dict[str, RouteSetting],
    ) -> RoutedConfig:
        """Return the ``RoutedConfig`` of ``catalog``, ``routes`` and these stages.

        Cross-entry checks (a route naming a model outside the catalog, an unpriced model,
        an unknown stage kind) stay with the gateway, which runs them at boot.

        Args:
            catalog: The catalog models, by id.
            routes: The routes, by name.

        Returns:
            The ``RoutedConfig`` of ``catalog``, ``routes`` and these stages.

        """
        return cast("Callable[..., RoutedConfig]", RoutedConfig)(
            catalog={model_id: m.as_mapping() for model_id, m in catalog.items()},
            routes={name: r.model_dump(mode="json") for name, r in routes.items()},
            **cast("dict[str, object]", self.model_dump()),
        )
