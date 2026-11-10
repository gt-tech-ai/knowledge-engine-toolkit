"""Unit tests for the config-selected ``Dataset`` factory.

They also cover its ``stub`` and ``jsonl`` backends.
"""

from __future__ import annotations

import hashlib
import json
from typing import TYPE_CHECKING
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.dataset import (
    DatasetConfig,
    DatasetKind,
    new_dataset_from_config,
)
from techai_webutils.clients.dataset.jsonl import JsonlDataset
from techai_webutils.clients.dataset.stub import StubDataset
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.dataset import Dataset

if TYPE_CHECKING:
    from pathlib import Path

    from techai_webutils.core.interfaces.dataset import EvalCase

_LINES = (
    '{"id": "c1", "inputs": {"query": "q1"}, "expected": {"answer": "a1"}}\n'
    '{"id": "c2", "inputs": {"query": "q2"}, "expected": {}, '
    '"metadata": {"slice": "s"}}\n'
)


async def _collect(dataset: Dataset) -> list[EvalCase]:
    """Return every case the dataset yields.

    Args:
        dataset: The dataset.

    Returns:
        Every case the dataset yields.

    """
    return [case async for case in dataset.cases()]


def test_new_dataset_from_config_returns_backend_per_kind(tmp_path: Path) -> None:
    """Test that each dataset kind builds its backend.

    **Why this test is important:**
      - Swapping the eval dataset must be a config change; a kind that built the wrong
        backend would silently evaluate against the wrong cases.

    **What it tests:**
      - the default config builds a ``StubDataset``
      - ``jsonl`` builds a ``JsonlDataset``
      - ``aiops`` builds whatever the injected ``backends["aiops"]`` factory returns

    Args:
        tmp_path: A fresh directory for the test.

    """
    path = tmp_path / "cases.jsonl"
    _ = path.write_text(_LINES)
    aiops = MagicMock(spec=Dataset)
    backends = {DatasetKind.AIOPS.value: lambda _config: aiops}

    assert isinstance(new_dataset_from_config(DatasetConfig()), StubDataset)
    jsonl_config = DatasetConfig(kind=DatasetKind.JSONL, path=str(path))
    assert isinstance(new_dataset_from_config(jsonl_config), JsonlDataset)
    aiops_config = DatasetConfig(kind=DatasetKind.AIOPS)
    assert new_dataset_from_config(aiops_config, backends=backends) is aiops


def test_new_dataset_from_config_unknown_kind_raises() -> None:
    """Test that an unknown kind, or an aiops kind with no backend, fails loudly.

    **Why this test is important:**
      - A typo in the dataset kind must stop the eval run at build time, not fall back to
        the stub and report a meaningless pass.

    **What it tests:**
      - an unknown kind raises a ``ValueError`` coded ``INVALID_INPUT``
      - ``aiops`` with no injected backend raises ``AppError(INVALID_INPUT)``
    """
    with pytest.raises(AppValueError, match="unknown dataset kind") as caught:
        _ = new_dataset_from_config(DatasetConfig(kind="nope"))
    assert isinstance(caught.value, AppError)
    assert caught.value.code is ErrorCode.INVALID_INPUT

    with pytest.raises(AppError) as missing:
        _ = new_dataset_from_config(DatasetConfig(kind=DatasetKind.AIOPS))
    assert missing.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_stub_dataset_cases_and_stable_version() -> None:
    """Test that the stub dataset is a fixed, stably versioned case set.

    **Why this test is important:**
      - The stub is the zero-infrastructure default an eval run boots on; its version keys
        the baseline, so it must not drift between reads or instances.

    **What it tests:**
      - two reads yield the same three cases
      - two instances report the same version, the sha256 hex of the cases
    """
    first, second = StubDataset(), StubDataset()

    cases = await _collect(first)
    assert [case.id for case in cases] == ["stub-1", "stub-2", "stub-3"]
    assert await _collect(first) == cases
    assert first.version() == second.version()
    assert len(first.version()) == len(hashlib.sha256().hexdigest())


@pytest.mark.asyncio
async def test_jsonl_dataset_version_is_content_hash(tmp_path: Path) -> None:
    """Test that a jsonl dataset's version is the sha256 of the file bytes.

    **Why this test is important:**
      - The version is how a run knows its baseline still applies; any edit to the file
        must change it, and nothing else may.

    **What it tests:**
      - ``version()`` equals ``sha256(file bytes)``
      - the cases parse in file order with ``metadata`` defaulting to empty

    Args:
        tmp_path: A fresh directory for the test.

    """
    path = tmp_path / "cases.jsonl"
    _ = path.write_text(_LINES)
    dataset = JsonlDataset(str(path))

    assert dataset.version() == hashlib.sha256(_LINES.encode()).hexdigest()
    cases = await _collect(dataset)
    assert [case.id for case in cases] == ["c1", "c2"]
    assert cases[0].inputs == {"query": "q1"}
    assert cases[0].expected == {"answer": "a1"}
    assert cases[0].metadata == {}
    assert cases[1].metadata == {"slice": "s"}


@pytest.mark.asyncio
async def test_jsonl_dataset_malformed_line_raises_invalid_input(tmp_path: Path) -> None:
    """Test that a malformed or missing dataset file fails with a coded error.

    **Why this test is important:**
      - A broken dataset must stop the run with a reason, never yield a partial case set
        that reports an inflated pass rate.

    **What it tests:**
      - a non-JSON line raises ``AppError(INVALID_INPUT)``
      - a line with no ``id``, a non-object line, or a non-object ``inputs`` raises
        ``AppError(INVALID_INPUT)``
      - a missing file raises ``AppError(UNAVAILABLE)``

    Args:
        tmp_path: A fresh directory for the test.

    """
    bad_json = tmp_path / "bad.jsonl"
    _ = bad_json.write_text(_LINES + "{not json\n")
    no_id = tmp_path / "no_id.jsonl"
    _ = no_id.write_text(json.dumps({"inputs": {}}) + "\n")
    array = tmp_path / "array.jsonl"
    _ = array.write_text("[1, 2]\n")
    bad_inputs = tmp_path / "bad_inputs.jsonl"
    _ = bad_inputs.write_text(json.dumps({"id": "c1", "inputs": ["q"]}) + "\n")

    for path in (bad_json, no_id, array, bad_inputs):
        with pytest.raises(AppError) as caught:
            _ = await _collect(JsonlDataset(str(path)))
        assert caught.value.code is ErrorCode.INVALID_INPUT

    with pytest.raises(AppError) as missing:
        _ = JsonlDataset(str(tmp_path / "absent.jsonl")).version()
    assert missing.value.code is ErrorCode.UNAVAILABLE
