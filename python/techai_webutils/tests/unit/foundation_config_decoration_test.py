"""Tests for the ``decoration`` config section and Go-style durations (Go ``foundation/config``)."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.config.duration import parse_duration


@pytest.mark.parametrize(
    ("raw", "seconds"),
    [
        ("30s", 30.0),
        ("1m30s", 90.0),
        ("250ms", 0.25),
        ("1h", 3600.0),
        ("-1.5s", -1.5),
        ("1\u00b5s", 1e-6),
        ("0", 0.0),
        (2, 2.0),
        (0.5, 0.5),
    ],
)
def test_parse_duration_reads_go_durations(raw: object, seconds: float) -> None:
    """A Go duration string, or a number of seconds, is read as seconds.

    Why this test is important:
      - Go and Python services share one layered YAML, so a timeout written for Go must mean
        the same to Python.

    What it tests:
      - Each unit, compound terms, a sign, ``"0"`` and plain numbers.
    """
    assert parse_duration(raw) == pytest.approx(seconds)


@pytest.mark.parametrize("raw", ["", "30", "1x", "s", "1s2", True, None, [1]])
def test_parse_duration_rejects_malformed_values(raw: object) -> None:
    """A malformed duration fails loudly with a coded error.

    Why this test is important:
      - A typo in a timeout must not silently become zero (no timeout).

    What it tests:
      - Missing units, unknown units, trailing junk, bools and non-scalars raise
        ``AppValueError``.
    """
    with pytest.raises(AppValueError, match="invalid duration"):
        parse_duration(raw)


def test_defaults_match_go() -> None:
    """``default_config`` decorates each tier as Go's ``DefaultConfig`` does.

    Why this test is important:
      - Running with no decoration section must keep today's behavior in both languages.

    What it tests:
      - Controllers and clients are observed, pipelines are not; repositories retry and break;
        the ``storage`` client is guarded with a 30 s timeout and a waiting bulkhead; a
        controller's rate limit and bulkhead are off, its bulkhead shedding once enabled.
    """
    config = decoration.default_config()

    assert decoration.on(config.controllers.layers_for("api").tracing)
    assert not decoration.on(config.pipelines.layers_for("p").tracing)
    repo = config.repositories.layers_for("users")
    assert decoration.on(repo.retry.enabled)
    assert decoration.on(repo.circuit_breaker.enabled)
    storage = config.clients.layers_for("storage")
    assert storage.timeout == pytest.approx(30.0)
    assert decoration.on(storage.bulkhead.enabled)
    assert decoration.on(storage.tracing)
    api = config.controllers.layers_for("api")
    assert not decoration.on(api.rate_limit.enabled)
    assert not decoration.on(api.bulkhead.enabled)
    assert decoration.on(api.bulkhead.shed)
    assert not decoration.on(storage.bulkhead.shed)
    assert config.tier("job") is config.jobs
    assert config.tier("gateway") is None


def test_from_mapping_overlays_defaults_and_overrides() -> None:
    """A section changes only what it sets, per tier default and per component.

    Why this test is important:
      - Decorators are tuned by config; a partial section must not reset everything else.

    What it tests:
      - A tier default timeout (``"2s"``) applies to every component; an override turns
        tracing off for one; a zero timeout override wins over the default; untouched tuning
        keeps its default.
    """
    config = decoration.from_mapping({
        "services": {
            "defaults": {"timeout": "2s"},
            "overrides": {
                "quiet": {"tracing": False, "timeout": 0},
                "fast": {"retry": {"enabled": True, "max_retries": 7}},
            },
        }
    })

    plain = config.services.layers_for("orders")
    assert plain.timeout == pytest.approx(2.0)
    quiet = config.services.layers_for("quiet")
    assert quiet.tracing is False
    assert quiet.timeout == pytest.approx(0.0)
    fast = config.services.layers_for("fast")
    assert fast.retry.max_retries == 7
    assert fast.retry.multiplier == pytest.approx(2.0)
    assert config.repositories == decoration.default_config().repositories


def test_from_mapping_reads_dotted_override_names() -> None:
    """An override of a dotted component name resolves under its full name.

    Why this test is important:
      - Components are named with dots (``outbox.sqs``); written nested by segment, an
        override must not become an empty override of its first segment, as Go's loader
        reads it.

    What it tests:
      - A dotted key and the same name nested by segment both resolve, a segment that is
        itself a component keeps its own override, and a pure segment is no override.
    """
    config = decoration.from_mapping({
        "clients": {
            "overrides": {
                "seed.ingress.gateway": {"timeout": "7s"},
                "outbox": {"logging": False, "s3": {"timeout": "4s"}},
                "seed": {"ingress": {"auth0": {"retry": {"enabled": True}}}},
            }
        }
    })

    overrides = config.clients.overrides
    assert overrides["seed.ingress.gateway"].timeout == pytest.approx(7.0)
    assert overrides["outbox"].logging is False
    assert overrides["outbox.s3"].timeout == pytest.approx(4.0)
    assert overrides["seed.ingress.auth0"].retry.enabled is True
    assert "seed" not in overrides
    assert config.clients.layers_for("outbox.s3").bulkhead.enabled is True


@pytest.mark.parametrize(
    "section",
    [
        "not a mapping",
        {"services": "nope"},
        {"services": {"overrides": {"x": {"tracing": "yes"}}}},
        {"services": {"defaults": {"timeout": "soon"}}},
        {"services": {"defaults": {"retry": {"max_retries": "many"}}}},
        {"services": {"overrides": "x"}},
    ],
)
def test_from_mapping_rejects_malformed_sections(section: object) -> None:
    """A malformed section fails loudly with a coded error naming its path.

    Why this test is important:
      - A config typo must stop the service at boot, not silently disable a layer.

    What it tests:
      - A non-mapping, a non-bool switch, a bad duration, a bad number and non-mapping
        overrides each raise ``AppValueError``.
    """
    with pytest.raises(AppValueError):
        decoration.from_mapping(section)


def test_load_reads_the_section_or_keeps_the_defaults() -> None:
    """``load`` reads the ``decoration`` key, keeping the defaults when it is absent.

    Why this test is important:
      - A service without a decoration section must boot with today's decoration.

    What it tests:
      - An absent section returns the defaults; a present one is overlaid.
    """
    loader = MagicMock()
    loader.get.return_value = None
    assert decoration.load(loader) == decoration.default_config()
    loader.get.return_value = {"jobs": {"defaults": {"logging": False}}}
    assert decoration.load(loader).jobs.defaults.logging is False
    loader.get.assert_called_with(decoration.SECTION_KEY)


@pytest.mark.parametrize(
    "tuning",
    [
        decoration.Retry(multiplier=0.5),
        decoration.CircuitBreaker(failure_ratio=2.0),
        decoration.Bulkhead(max_concurrent=0, backoff_ratio=0.5),
        decoration.RateLimit(rate=-1.0),
    ],
)
def test_validate_rejects_out_of_range_tuning(
    tuning: decoration.Retry
    | decoration.CircuitBreaker
    | decoration.Bulkhead
    | decoration.RateLimit,
) -> None:
    """Each layer's tuning rejects an out-of-range value.

    Why this test is important:
      - A nonsensical tuning (a shrinking backoff, a ratio over 1) must fail at boot.

    What it tests:
      - ``validate`` raises ``AppValueError`` for each layer's bad value.
    """
    with pytest.raises(AppValueError):
        tuning.validate()


def test_tier_overlay_merges_overrides_by_name() -> None:
    """Overlaying a tier merges same-name overrides and keeps the others.

    Why this test is important:
      - Layered config files (base, env overlay) each contribute overrides.

    What it tests:
      - Overlaying ``a`` onto ``a`` + ``b`` merges ``a``'s switches and keeps ``b``.
    """
    base = decoration.Tier(
        overrides={
            "a": decoration.Layers(tracing=True),
            "b": decoration.Layers(logging=True),
        }
    )
    over = decoration.Tier(overrides={"a": decoration.Layers(metrics=True)})

    merged = base.overlay(over)

    assert merged.overrides["a"] == decoration.Layers(tracing=True, metrics=True)
    assert merged.overrides["b"] == decoration.Layers(logging=True)


def test_from_mapping_reads_a_null_section_as_its_default() -> None:
    """A YAML ``null`` (an empty key) reads as the field's default, nested ones included.

    Why this test is important:
      - An empty ``retry:`` key in a layered YAML file is ``None``; read as ``None`` it would
        crash the first decorated call instead of meaning "no change".

    What it tests:
      - A null tier and a null nested layer section both decode as their defaults, so the
        resolved layers match the untouched defaults.
    """
    config = decoration.from_mapping({
        "jobs": None,
        "services": {"defaults": {"retry": None, "bulkhead": None}},
    })

    default = decoration.default_config().services.layers_for("orders")
    assert config.services.layers_for("orders") == default
    assert config.jobs.layers_for("j") == decoration.default_config().jobs.layers_for("j")
