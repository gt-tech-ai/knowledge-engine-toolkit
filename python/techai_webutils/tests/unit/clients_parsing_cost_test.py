"""Tests for the parse-cost capability and the isolated parser's injected executor."""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import TYPE_CHECKING
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.parsing.factory import ParserConfig, parser_from_config
from techai_webutils.clients.parsing.isolated.isolated import IsolatedParser
from techai_webutils.core.domain import DocumentFormat, ParsedDocument
from techai_webutils.core.interfaces.parser import CostedParser, ParseCost

if TYPE_CHECKING:
    from collections.abc import Callable

_SRC = Path(__file__).resolve().parents[2] / "src" / "techai_webutils"


def test_isolated_parser_reports_its_memory_cap_as_its_cost() -> None:
    """The isolated parser's declared cost is its configured per-parse memory cap and one CPU.

    Why this test is important:
      - Lanes and Ray tasks size themselves from this cost; a cost that disagreed with the real cap
        would overcommit memory and get the pod OOM-killed.

    What it tests:
      - ``parser_from_config(ParserConfig(memory_bytes=N))`` is a ``CostedParser`` whose
        ``cost()`` is ``ParseCost(memory_bytes=N, cpu=1.0)``.
    """
    parser = parser_from_config(ParserConfig(memory_bytes=768 * 1024 * 1024))

    assert isinstance(parser, CostedParser)
    assert parser.cost() == ParseCost(memory_bytes=768 * 1024 * 1024, cpu=1.0)


@pytest.mark.asyncio
async def test_isolated_parser_uses_injected_executor(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Both parse entry points run the blocking subprocess wait on the injected executor.

    Why this test is important:
      - The executor bounds how many child parsers run at once; if the parser fell back to the
        event loop's default pool, a lane sized to its pod could still start ~CPU+4 parses at once.

    What it tests:
      - With the child run in-process over a stubbed parse, ``parse`` and ``parse_path`` each submit
        exactly one call to the injected executor and return the parsed document.
    """
    module = "techai_webutils.clients.parsing.isolated.isolated"
    parsed = ParsedDocument(markdown_content="# A", document_format=DocumentFormat.HTML)

    def _parse(*_args: object) -> ParsedDocument:
        return parsed

    def _run_in_process(
        fn: Callable[..., object], *args: object, **_kwargs: object
    ) -> object:
        return fn(*args)

    monkeypatch.setattr(f"{module}._parse_document", _parse)
    monkeypatch.setattr(f"{module}._parse_document_path", _parse)
    monkeypatch.setattr(f"{module}.run_isolated", _run_in_process)
    pool = ThreadPoolExecutor(max_workers=1)
    submit = MagicMock(wraps=pool.submit)
    monkeypatch.setattr(pool, "submit", submit)
    parser = parser_from_config(ParserConfig(), executor=pool)

    results = [
        await parser.parse(b"<html></html>", filename="a.html"),
        await parser.parse_path("/nowhere/a.html", filename="a.html"),
    ]
    pool.shutdown()

    assert submit.call_count == 2
    assert results == [parsed, parsed]


def test_no_default_executor_remains_on_the_parse_path() -> None:
    """No parse-path module hands work to the event loop's implicit default executor.

    Why this test is important:
      - ``run_in_executor(None, …)`` silently bypasses the lane's sized executor; this pins that
        the parse path only ever uses the injected one.

    What it tests:
      - No file under ``clients/parsing`` contains ``run_in_executor(None``.
    """
    offenders = [
        str(path.relative_to(_SRC))
        for path in (_SRC / "clients" / "parsing").rglob("*.py")
        if "run_in_executor(None" in path.read_text()
    ]

    assert offenders == []


def test_isolated_parser_default_keeps_the_loop_executor() -> None:
    """Without an injected executor the parser keeps today's behaviour: the loop's default pool.

    Why this test is important:
      - Consumers that have not wired a sized executor yet must see no change in throughput or
        cost from this release.

    What it tests:
      - ``IsolatedParser()`` has no executor of its own (``executor is None``).
    """
    assert IsolatedParser().executor is None
