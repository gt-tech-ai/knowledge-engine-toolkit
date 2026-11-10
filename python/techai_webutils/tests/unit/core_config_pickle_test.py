"""Config compatibility guard: every ``*Config`` dataclass pickles.

None gains a required field.

Ray ships configs to its workers by pickle, so a worker on one release can unpickle a
config built by another. A field added without a default breaks that: an older pickle has
no value for it. The baseline ``core_config_required_fields.json`` lists every config
class with the fields it already requires (and a value for each); a story that adds a
config class adds it there, and a class may never gain a required field the baseline does
not already record.
"""

from __future__ import annotations

import dataclasses
import importlib
import json
import pkgutil
from functools import cache
from multiprocessing.reduction import ForkingPickler
from pathlib import Path
from typing import TYPE_CHECKING, cast

import techai_webutils

if TYPE_CHECKING:
    from _typeshed import DataclassInstance

_BASELINE_PATH = Path(__file__).with_name("core_config_required_fields.json")
"""The required-fields baseline beside this test."""

_CONFIG_REF = "$config"
"""Baseline key marking a field whose value is another baselined config, built
recursively."""

type Baseline = dict[str, dict[str, object]]
"""Config class path → {required field → value or {"$config": class path}}."""


def _class_path(cls: type) -> str:
    """Return ``module.QualName`` for ``cls``, the baseline's key.

    Args:
        cls: The config class.

    Returns:
        ``module.QualName`` for ``cls``, the baseline's key.

    """
    return f"{cls.__module__}.{cls.__qualname__}"


@cache
def _discover_configs() -> dict[str, type[DataclassInstance]]:
    """Import every ``techai_webutils`` module.

    Return its ``*Config`` dataclasses by path.

    Returns:
        The config classes by module path.

    """
    found: dict[str, type[DataclassInstance]] = {}
    for info in pkgutil.walk_packages(techai_webutils.__path__, "techai_webutils."):
        module = importlib.import_module(info.name)
        for name, value in cast("dict[str, object]", vars(module)).items():
            if (
                isinstance(value, type)
                and name.endswith("Config")
                and value.__module__ == module.__name__
                and dataclasses.is_dataclass(value)
            ):
                found[_class_path(value)] = value
    return found


def _load_baseline() -> Baseline:
    """Read the required-fields baseline.

    Returns:
        The required-fields baseline.

    """
    return cast("Baseline", json.loads(_BASELINE_PATH.read_text()))


def required_fields(cls: type[DataclassInstance]) -> set[str]:
    """Return the names of ``cls``'s init fields that have no default.

    Nor a factory.

    Args:
        cls: The config class.

    Returns:
        The names of ``cls``'s init fields.

    """
    return {
        field.name
        for field in dataclasses.fields(cls)
        if field.init
        and field.default is dataclasses.MISSING
        and field.default_factory is dataclasses.MISSING
    }


def unbaselined_required(
    cls: type[DataclassInstance], baseline_entry: dict[str, object]
) -> list[str]:
    """Return ``cls``'s required fields the baseline entry does not record, sorted.

    Args:
        cls: The config class.
        baseline_entry: The fields the baseline fills.

    Returns:
        ``cls``'s required fields the baseline entry does not record, sorted.

    """
    return sorted(required_fields(cls) - baseline_entry.keys())


def build_config(path: str, baseline: Baseline) -> DataclassInstance:
    """Build the config at ``path`` from its baseline values.

    It resolves ``$config`` references.

    Args:
        path: The config class's dotted path.
        baseline: Values for the required fields.

    Returns:
        The config at ``path`` from its baseline values.

    """
    kwargs: dict[str, object] = {}
    for field_name, value in baseline[path].items():
        if isinstance(value, dict) and _CONFIG_REF in value:
            ref = cast("dict[str, str]", value)[_CONFIG_REF]
            kwargs[field_name] = build_config(ref, baseline)
        else:
            kwargs[field_name] = value
    return _discover_configs()[path](**kwargs)


def test_every_config_class_is_in_the_baseline() -> None:
    """The baseline lists exactly the config classes the package defines.

    Why this test is important:
      - The required-field check only covers classes in the baseline; a new config class
        missing from it would escape the guard, and a stale entry hides a removed or
        renamed class.

    What it tests:
      - The discovered ``*Config`` dataclass paths equal the baseline's keys.
    """
    assert set(_discover_configs()) == set(_load_baseline())


def test_no_config_class_gained_a_required_field() -> None:
    """No baselined config class requires a field the baseline does not already record.

    Why this test is important:
      - A required field added to a config breaks every pickle an older release made of
        it, which is how Ray ships configs to workers (failure mode DP1: workers fail to
        unpickle their task).

    What it tests:
      - For every discovered config class, its required fields are a subset of its
        baseline entry.
    """
    baseline = _load_baseline()
    drift = {
        path: missing
        for path, cls in _discover_configs().items()
        if (missing := unbaselined_required(cls, baseline.get(path, {})))
    }
    assert drift == {}


def test_every_config_survives_a_pickle_round_trip() -> None:
    """Every config built from its baseline values pickles and unpickles.

    The result equals the original.

    Why this test is important:
      - Ray moves configs between processes by pickle; a config holding an unpicklable
        default (a lambda, a lock, a client) would fail only when a job is first fanned
        out to a cluster.

    What it tests:
      - Every baselined config class survives a ``ForkingPickler`` round trip.
    """
    baseline = _load_baseline()
    for path in sorted(baseline):
        config = build_config(path, baseline)
        assert ForkingPickler.loads(ForkingPickler.dumps(config)) == config, path


def test_a_new_required_field_is_reported() -> None:
    """A config that gains a required field its baseline entry lacks is reported by name.

    Why this test is important:
      - Proves the guard can fail: without it, a check that never reports drift would pass
        forever.

    What it tests:
      - A fixture config with a defaulted and a required field, against a baseline entry
        recording neither, reports exactly the required one; recording it clears the
        report.
    """

    @dataclasses.dataclass(frozen=True, slots=True)
    class FixtureConfig:
        """A config that gained a required field after its baseline was recorded."""

        region: str
        """The newly required field."""
        timeout_seconds: float = 5.0
        """A defaulted field, which never breaks an older pickle."""

    assert unbaselined_required(FixtureConfig, {}) == ["region"]
    assert unbaselined_required(FixtureConfig, {"region": "us-east-1"}) == []
