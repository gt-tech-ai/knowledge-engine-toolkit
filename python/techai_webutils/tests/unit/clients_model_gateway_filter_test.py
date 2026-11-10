"""Unit and property tests for the routed gateway's deterministic filter stage."""

from __future__ import annotations

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.model_gateway.routed.catalog import ModelCatalog
from techai_webutils.clients.model_gateway.routed.filter import (
    FilterResult,
    filter_candidates,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.types.routing import (
    Capabilities,
    CatalogEndpointRef,
    RouteRequirements,
    RoutingPolicy,
)

_EXPECTED_KEPT_COUNT = 3

_GEN = RouteContext(tenant_id="org-1", step="generate")
_SMALL_REQ = RouteRequirements(prompt_tokens=100, max_tokens=100)


def _refs(catalog_raw: dict[str, object]) -> tuple[CatalogEndpointRef, ...]:
    """Return the fixture catalog's endpoint refs.

    Returns:
        The fixture catalog's endpoint refs.

    """
    return ModelCatalog.from_mapping(catalog_raw, priced_ids=None).refs


def test_pin_still_honours_data_policy(catalog_raw: dict[str, object]) -> None:
    """Test that a pin cannot route around residency rules.

    **Why this test is important:**
      - A tenant pin is an admin's convenience, not a way to send data out of region.

    **What it tests:**
      - a pin to Sonnet under ``region=us-east-1`` keeps only ``sonnet@use1``, decider
        ``admin_pin``
      - a pin to the off-cloud model without ``allow_off_cloud`` raises ``FORBIDDEN``
    """
    refs = _refs(catalog_raw)
    pinned = filter_candidates(
        refs,
        RoutingPolicy(pins={"generate": "anthropic/sonnet"}, region="us-east-1"),
        _GEN,
        _SMALL_REQ,
    )

    assert [ref.key for ref in pinned.kept] == ["anthropic/sonnet@use1"]
    assert (pinned.direct_model, pinned.decider) == ("anthropic/sonnet", "admin_pin")
    assert pinned.rejections["anthropic/sonnet@usw2"] == "data_policy"
    with pytest.raises(AppError) as caught:
        _ = filter_candidates(
            refs, RoutingPolicy(pins={"*": "meta/small"}), _GEN, _SMALL_REQ
        )
    assert caught.value.code is ErrorCode.FORBIDDEN


def test_filter_drops_models_whose_context_window_is_too_small(
    catalog_raw: dict[str, object],
) -> None:
    """Test that a prompt too large for a model's window drops that model for capability.

    **Why this test is important:**
      - Sending a 20k-token prompt to an 8k model fails at the provider after the cost of
        a round trip; the filter must drop it first.

    **What it tests:**
      - with ``allow_off_cloud`` on, a 20k prompt rejects ``meta/small@local`` as
        ``capability`` and keeps the three Claude endpoints; no direct choice is made
    """
    result = filter_candidates(
        _refs(catalog_raw),
        RoutingPolicy(allow_off_cloud=True),
        _GEN,
        RouteRequirements(prompt_tokens=20_000, max_tokens=1000),
    )

    assert result.rejections == {"meta/small@local": "capability"}
    assert len(result.kept) == _EXPECTED_KEPT_COUNT
    assert (result.direct_model, result.decider) == (None, "local")


def test_filter_empty_policy_raises_forbidden(catalog_raw: dict[str, object]) -> None:
    """Test that policy excluding every endpoint is ``FORBIDDEN``, not a silent default.

    **Why this test is important:**
      - Falling back to some model when policy allows none would break the tenant's rules.

    **What it tests:**
      - a region no endpoint runs in raises ``AppError(FORBIDDEN)``
    """
    with pytest.raises(AppError) as caught:
        _ = filter_candidates(
            _refs(catalog_raw), RoutingPolicy(region="eu-central-1"), _GEN, _SMALL_REQ
        )

    assert caught.value.code is ErrorCode.FORBIDDEN


def test_filter_no_model_fits_raises_invalid_input(
    catalog_raw: dict[str, object],
) -> None:
    """Test that a request no model can serve is ``INVALID_INPUT``.

    **Why this test is important:**
      - A request bigger than every window is the caller's problem, not a policy denial.

    **What it tests:**
      - a 500k-token prompt raises ``AppError(INVALID_INPUT)``
    """
    with pytest.raises(AppError) as caught:
        _ = filter_candidates(
            _refs(catalog_raw),
            RoutingPolicy(allow_off_cloud=True),
            _GEN,
            RouteRequirements(prompt_tokens=500_000),
        )

    assert caught.value.code is ErrorCode.INVALID_INPUT


def test_user_mode_honours_explicit_model_choice_within_policy(
    catalog_raw: dict[str, object],
) -> None:
    """Test that an end user's model choice is served directly in ``user`` mode.

    **Why this test is important:**
      - In user mode people decide; their explicit choice must beat the planner and a pin.

    **What it tests:**
      - ``model_preference=anthropic/haiku`` keeps only Haiku with decider ``user``, even
        with a Sonnet pin present
    """
    ctx = RouteContext(
        tenant_id="org-1", step="generate", model_preference="anthropic/haiku"
    )

    result = filter_candidates(
        _refs(catalog_raw),
        RoutingPolicy(pins={"generate": "anthropic/sonnet"}),
        ctx,
        _SMALL_REQ,
    )

    assert [ref.key for ref in result.kept] == ["anthropic/haiku@use1"]
    assert (result.direct_model, result.decider) == ("anthropic/haiku", "user")


def test_user_choice_outside_allow_list_raises_forbidden(
    catalog_raw: dict[str, object],
) -> None:
    """Test that a user's choice outside policy is refused, never substituted.

    **Why this test is important:**
      - Quietly serving a different model than the one asked for misleads the user.

    **What it tests:**
      - choosing Sonnet when only Haiku is allowed raises ``FORBIDDEN``
      - choosing a model not in the catalog raises ``INVALID_INPUT``
    """
    refs = _refs(catalog_raw)
    allow_haiku = RoutingPolicy(allow={"*": ("anthropic/haiku",)})

    with pytest.raises(AppError) as denied:
        _ = filter_candidates(
            refs,
            allow_haiku,
            RouteContext(step="generate", model_preference="anthropic/sonnet"),
            _SMALL_REQ,
        )
    with pytest.raises(AppError) as unknown:
        _ = filter_candidates(
            refs,
            RoutingPolicy(),
            RouteContext(step="generate", model_preference="openai/none"),
            _SMALL_REQ,
        )

    assert denied.value.code is ErrorCode.FORBIDDEN
    assert unknown.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.parametrize(("user_override", "decider"), [(False, "local"), (True, "user")])
def test_overlord_mode_ignores_user_choice_unless_user_override(
    catalog_raw: dict[str, object], *, user_override: bool, decider: str
) -> None:
    """Test that overlord mode hands the choice.

    To the planner unless overrides are allowed.

    **Why this test is important:**
      - The routing mode is one policy field; it must decide whether people or the planner
        choose.

    **What it tests:**
      - without ``user_override`` the preference is ignored (no direct model, all
        candidates kept, decider ``local``); with it, the preference is served (decider
        ``user``)
    """
    result = filter_candidates(
        _refs(catalog_raw),
        RoutingPolicy(mode="overlord", user_override=user_override),
        RouteContext(step="generate", model_preference="anthropic/haiku"),
        _SMALL_REQ,
    )

    assert result.decider == decider
    assert (result.direct_model is None) is (not user_override)


_REGIONS = st.sampled_from(["us-east-1", "us-west-2"])
_MODEL_IDS = st.sampled_from(["m/a", "m/b", "m/c"])
_REF = st.builds(
    CatalogEndpointRef,
    model_id=_MODEL_IDS,
    endpoint=st.sampled_from(["e1", "e2"]),
    capabilities=st.builds(
        Capabilities,
        context_tokens=st.integers(1000, 50_000),
        max_output=st.just(1000),
        streaming=st.booleans(),
        tools=st.booleans(),
    ),
    quality=st.just(0.5),
    ttft_ms=st.just(100),
    region=_REGIONS,
    zero_retention=st.booleans(),
    leaves_cloud_boundary=st.booleans(),
)
_POLICY = st.builds(
    RoutingPolicy,
    deny=st.dictionaries(st.just("*"), st.tuples(_MODEL_IDS), max_size=1),
    region=st.one_of(st.just(""), _REGIONS),
    zero_retention=st.booleans(),
    allow_off_cloud=st.booleans(),
)
_REQ = st.builds(
    RouteRequirements,
    prompt_tokens=st.integers(0, 60_000),
    streaming=st.booleans(),
    tools=st.booleans(),
)


@given(
    refs=st.lists(_REF, max_size=6, unique_by=lambda r: (r.model_id, r.endpoint)),
    policy=_POLICY,
    req=_REQ,
)
def test_filter_never_returns_policy_violating_endpoint(
    refs: list[CatalogEndpointRef], policy: RoutingPolicy, req: RouteRequirements
) -> None:
    """Test that no kept endpoint ever violates capability, data policy or the deny list.

    **Why this test is important:**
      - This is the routing security invariant; generated catalogs and policies reach the
        corner cases hand-written examples miss.

    **What it tests:**
      - every kept endpoint fits the request, runs in the policy's region, honours zero
        retention and the off-cloud rule, and is not denied; an empty result raises a
        coded ``FORBIDDEN`` or ``INVALID_INPUT``
    """
    error: AppError | None = None
    result: FilterResult | None = None
    try:
        result = filter_candidates(
            tuple(refs), policy, RouteContext(step="generate"), req
        )
    except AppError as exc:
        error = exc
    if error is not None:
        assert error.code in {ErrorCode.FORBIDDEN, ErrorCode.INVALID_INPUT}
        return
    assert result is not None
    denied = policy.denied("generate")
    for ref in result.kept:
        assert ref.capabilities.context_tokens >= req.prompt_tokens + req.max_tokens
        assert ref.capabilities.streaming or not req.streaming
        assert ref.capabilities.tools or not req.tools
        assert not policy.region or ref.region == policy.region
        assert ref.zero_retention or not policy.zero_retention
        assert policy.allow_off_cloud or not ref.leaves_cloud_boundary
        assert ref.model_id not in denied
