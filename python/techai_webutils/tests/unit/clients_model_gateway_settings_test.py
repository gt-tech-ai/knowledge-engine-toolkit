"""Unit tests for the routed gateway's typed config models (``routed/settings.py``)."""

from __future__ import annotations

from typing import cast

import pytest
from pydantic import ValidationError

from techai_webutils.clients.llm.builder import LlmKind
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.model_gateway import (
    GatewayConfig,
    GatewayKind,
    new_model_gateway_from_config,
)
from techai_webutils.clients.model_gateway.builder import ModelGatewayDependencies
from techai_webutils.clients.model_gateway.routed import RoutedGateway
from techai_webutils.clients.model_gateway.routed.catalog import ModelCatalog
from techai_webutils.clients.model_gateway.routed.settings import (
    CatalogModelSetting,
    EndpointLlmSetting,
    RoutedStagesSetting,
    RouteSetting,
)
from techai_webutils.core.types.routing import Route
from tests.json_path import dig


def _catalog(raw: dict[str, object]) -> dict[str, CatalogModelSetting]:
    """Validate a raw catalog into its typed models.

    Args:
        raw: The settings.

    Returns:
        The models by key.

    """
    return {k: CatalogModelSetting.model_validate(v) for k, v in raw.items()}


def test_typed_config_builds_the_same_gateway_as_the_raw_mappings(
    catalog_raw: dict[str, object], routes_raw: dict[str, object]
) -> None:
    """Test that the typed catalog, routes and stages assemble a ``RoutedConfig``.

    The config boots.

    **Why this test is important:**
      - A consumer moves its settings from untyped mappings to these models; the gateway
        it boots must see exactly the catalog it saw before.

    **What it tests:**
      - ``RoutedStagesSetting.routed_config`` over the typed fixture catalog and routes
        boots a ``RoutedGateway`` whose catalog refs equal those parsed from the raw
        mapping
      - the stage fields (default route, ranker block, attempt bound) carry through

    Args:
        catalog_raw: A catalog: Sonnet (two regions), Haiku.
        routes_raw: Routes named for the steps: generate → Sonnet (then Haiku).

    """
    stages = RoutedStagesSetting.model_validate({
        "default_route": "generate",
        "ranker": {"kind": "static_order"},
        "max_attempts": 3,
    })
    routes = {k: RouteSetting.model_validate(v) for k, v in routes_raw.items()}

    config = stages.routed_config(_catalog(catalog_raw), routes)
    gateway = new_model_gateway_from_config(
        GatewayConfig(kind=GatewayKind.ROUTED, routed=config),
        options=ModelGatewayDependencies(
            provider_factory=lambda llm: StubLlmProvider(llm.model), priced_ids=None
        ),
    )

    assert isinstance(gateway, RoutedGateway)
    typed = ModelCatalog.from_mapping(config.catalog, priced_ids=None)
    raw = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    assert typed == raw
    assert (config.default_route, config.ranker, config.max_attempts) == (
        "generate",
        {"kind": "static_order"},
        3,
    )


@pytest.mark.parametrize(
    ("path", "patch"),
    [
        ("quality", {"quality": 1.5}),
        ("endpoints", {"endpoints": []}),
        ("endpoints.0.llm.modle", {"endpoints": [{"name": "a", "llm": {"modle": "x"}}]}),
        ("endpoints.0.llm.kind", {"endpoints": [{"name": "a", "llm": {"kind": "gpt"}}]}),
        ("capabilities.context_tokens", {"capabilities": {"max_output": 1}}),
        ("price", {"price": 1}),
    ],
)
def test_a_defective_catalog_model_fails_naming_its_path(
    catalog_raw: dict[str, object], path: str, patch: dict[str, object]
) -> None:
    """Test that a bad catalog entry fails validation with the offending path.

    **Why this test is important:**
      - A typo in a catalog YAML must stop the settings load with a message that says
        where, not reach the gateway or the first request.

    **What it tests:**
      - an out-of-range prior, no endpoints, an unknown or misspelled ``llm`` field, an
        unknown provider kind, a missing capability and an unknown key each raise
        ``ValidationError`` whose message names the path

    Args:
        catalog_raw: A catalog: Sonnet (two regions), Haiku.
        path: The path the error names.
        patch: The defect to apply.

    """
    raw = {**cast("dict[str, object]", catalog_raw["anthropic/sonnet"]), **patch}

    with pytest.raises(ValidationError) as excinfo:
        _ = CatalogModelSetting.model_validate(raw)

    assert path in str(excinfo.value)


@pytest.mark.parametrize(
    "raw",
    [{"max_attempts": 0}, {"breaker_recovery_s": 0}, {"planer": {"kind": "local"}}],
)
def test_defective_stages_fail_validation(raw: dict[str, object]) -> None:
    """Test that a misspelled stage or an out-of-range bound fails the stages' validation.

    **Why this test is important:**
      - A misspelled stage key would otherwise be dropped silently and the default stage
        would run in its place.

    **What it tests:**
      - zero attempts, a zero breaker recovery and an unknown key each raise
        ``ValidationError``

    Args:
        raw: The settings.

    """
    with pytest.raises(ValidationError):
        _ = RoutedStagesSetting.model_validate(raw)


def test_route_setting_validates_and_builds_the_route() -> None:
    """Test that a route entry validates its bounds and becomes the engine's ``Route``.

    **Why this test is important:**
      - Routes are keyed by name in the YAML; the entry must carry everything else.

    **What it tests:**
      - an entry becomes a ``Route`` named for its key with every field carried
      - a threshold above 1 raises ``ValidationError``
    """
    entry = RouteSetting.model_validate({
        "default_model": "a/b",
        "preferred": ["c/d"],
        "exemplars": ["summarize"],
        "threshold": 0.4,
        "quality_floor": 0.2,
    })

    assert entry.route("generate") == Route(
        "generate", "a/b", ("c/d",), ("summarize",), 0.4, 0.2
    )
    with pytest.raises(ValidationError):
        _ = RouteSetting.model_validate({"threshold": 2})


def test_llm_defaults_fill_only_what_an_endpoint_leaves_unset() -> None:
    """Test that an endpoint inherits the consumer's LLM defaults field by field.

    **Why this test is important:**
      - The catalog names models only; the connection (kind, region, host) follows the
        consumer's LLM tier, so one config change moves every endpoint, yet an endpoint
        that sets a field keeps it.

    **What it tests:**
      - an endpoint naming only its model takes the defaults' kind and region
      - an endpoint naming its own region keeps it
      - ``served_models`` lists each endpoint's provider model id
      - the ``json`` capability round-trips under its config key
    """
    model = CatalogModelSetting.model_validate({
        "capabilities": {"context_tokens": 1000, "max_output": 100, "json": True},
        "quality": 0.5,
        "ttft_ms": 100,
        "endpoints": [
            {"name": "a", "llm": {"model": "m-a"}},
            {"name": "b", "llm": {"model": "m-b", "region": "us-west-2"}},
        ],
    })
    defaults = EndpointLlmSetting(kind=LlmKind.BEDROCK, region="us-east-1")

    filled = model.with_llm_defaults(defaults).as_mapping()

    assert [e["llm"] for e in cast("list[dict[str, object]]", filled["endpoints"])] == [
        {"kind": "bedrock", "region": "us-east-1", "model": "m-a"},
        {"kind": "bedrock", "region": "us-west-2", "model": "m-b"},
    ]
    assert model.served_models() == ("m-a", "m-b")
    assert dig(filled, "capabilities", "json") is True
