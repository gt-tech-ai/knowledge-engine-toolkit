"""Unit tests for the routed gateway's model catalog and route parsing (validated at boot)."""

from __future__ import annotations

import copy
from typing import Any

import pytest

from techai_webutils.clients.llm.builder import LlmKind
from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.core.errors import AppError, ErrorCode

_PRICED = frozenset({"anthropic/sonnet", "anthropic/haiku", "meta/small"})


def _break(raw: dict[str, Any], case: str) -> dict[str, Any]:
    """Return a copy of ``raw`` with one boot-time defect named by ``case``."""
    broken = copy.deepcopy(raw)
    sonnet = broken["anthropic/sonnet"]
    if case == "unknown_kind":
        sonnet["endpoints"][0]["llm"]["kind"] = "carrier-pigeon"
    elif case == "duplicate_endpoint":
        sonnet["endpoints"][1]["name"] = "use1"
    elif case == "missing_llm":
        del sonnet["endpoints"][0]["llm"]
    return broken


@pytest.mark.parametrize(
    ("case", "priced", "names"),
    [
        ("unknown_kind", _PRICED, "anthropic/sonnet"),
        ("duplicate_endpoint", _PRICED, "anthropic/sonnet@use1"),
        ("missing_llm", _PRICED, "anthropic/sonnet@use1"),
        ("unpriced", frozenset({"anthropic/sonnet", "meta/small"}), "anthropic/haiku"),
    ],
)
def test_catalog_rejects_invalid_entries_at_boot(
    catalog_raw: dict[str, Any], case: str, priced: frozenset[str], names: str
) -> None:
    """Test that a defective catalog fails the boot, naming the bad entry.

    **Why this test is important:**
      - A bad catalog must stop the service at start-up, not fail the first request that
        happens to route to the broken entry.

    **What it tests:**
      - an unknown ``LlmKind``, a duplicate endpoint, an endpoint without ``llm`` and an id
        missing from the price table each raise ``AppError(INVALID_INPUT)`` naming the entry
    """
    with pytest.raises(AppError) as caught:
        ModelCatalog.from_mapping(_break(catalog_raw, case), priced_ids=priced)

    assert caught.value.code is ErrorCode.INVALID_INPUT
    assert names in str(caught.value)


def test_catalog_parses_refs_in_order_and_strips_fallback(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a valid catalog yields its endpoints in order with their provider configs.

    **Why this test is important:**
      - Catalog order is the static ranker's tie-break, and the routed chain replaces the LLM
        tier's own fallback, so an endpoint config must never carry one.

    **What it tests:**
      - the refs are ``sonnet@use1, sonnet@usw2, haiku@use1, small@local`` with their priors
      - an endpoint's ``LlmConfig`` keeps kind and model and has ``fallback_model`` cleared
      - ``priced_ids=None`` skips the price check (an empty dev price table)
      - routes parse with their default and preferred models; an unknown model id is rejected
    """
    catalog_raw["anthropic/sonnet"]["endpoints"][0]["llm"]["fallback_model"] = "x"
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    routes = parse_routes(routes_raw, catalog)

    assert [ref.key for ref in catalog.refs] == [
        "anthropic/sonnet@use1",
        "anthropic/sonnet@usw2",
        "anthropic/haiku@use1",
        "meta/small@local",
    ]
    first = catalog.refs[0]
    assert (first.quality, first.ttft_ms, first.capabilities.tools) == (0.9, 900, True)
    llm = catalog.llm_config(first)
    assert (llm.kind, llm.model, llm.fallback_model) == (LlmKind.STUB, "sonnet-use1", "")
    assert routes["generate"].preferred == ("anthropic/haiku",)
    with pytest.raises(AppError) as caught:
        parse_routes({"generate": {"default_model": "openai/none"}}, catalog)
    assert caught.value.code is ErrorCode.INVALID_INPUT


_ENDPOINT = {"name": "e", "llm": {"kind": "stub", "model": "m"}}
_CAPS = {"context_tokens": 1000, "max_output": 100}


@pytest.mark.parametrize(
    "raw",
    [
        [],
        {},
        {"m/a": "not a mapping"},
        {"m/a": {"capabilities": _CAPS, "ttft_ms": 1, "endpoints": [_ENDPOINT]}},
        {"m/a": {"capabilities": _CAPS, "quality": 1, "ttft_ms": 1, "endpoints": []}},
        {
            "m/a": {
                "capabilities": _CAPS,
                "quality": 1,
                "ttft_ms": 1,
                "endpoints": [{"name": "e", "llm": {"kind": "stub", "colour": "red"}}],
            }
        },
    ],
    ids=[
        "not-mapping",
        "empty",
        "model-not-mapping",
        "no-quality",
        "no-endpoints",
        "llm-field",
    ],
)
def test_catalog_rejects_malformed_shapes(raw: object) -> None:
    """Test that structurally malformed catalogs are coded errors, not crashes.

    **Why this test is important:**
      - The catalog is hand-written YAML; a shape mistake must read as a named config error.

    **What it tests:**
      - each malformed shape raises ``AppError(INVALID_INPUT)``
    """
    with pytest.raises(AppError) as caught:
        ModelCatalog.from_mapping(raw, priced_ids=None)  # type: ignore[arg-type]

    assert caught.value.code is ErrorCode.INVALID_INPUT


def test_routes_reject_a_non_list_preference(catalog_raw: dict[str, Any]) -> None:
    """Test that a route's ``preferred`` must be a list of model ids.

    **Why this test is important:**
      - A bare string would be read character by character as model ids.

    **What it tests:**
      - ``preferred: "anthropic/haiku"`` raises ``AppError(INVALID_INPUT)``
    """
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)

    with pytest.raises(AppError) as caught:
        parse_routes(
            {
                "generate": {
                    "default_model": "anthropic/sonnet",
                    "preferred": "anthropic/haiku",
                }
            },
            catalog,
        )

    assert caught.value.code is ErrorCode.INVALID_INPUT
