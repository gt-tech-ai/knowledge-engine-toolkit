"""Tests for the document-parser client tier: the ``parser_from_config`` factory + lazy heavy deps.

Why these tests are important:
  - The factory selects a parse backend by ``kind`` (mirroring the Go ``NewFromConfig`` contract) and
    MUST fail loudly on an unknown kind rather than return a broken client — the composition root
    relies on that fail-loud behaviour.
  - requires the heavy ``markitdown``/``pymupdf`` deps stay lazy: importing
    ``techai_webutils.clients.parsing`` (and building the default backend) must NOT pull them, so a
    deployable that never parses never pays the import.
"""

from __future__ import annotations

import subprocess
import sys
from unittest.mock import patch

import pytest

from techai_webutils.clients.parsing import ParserConfig, ParserKind, parser_from_config
from techai_webutils.clients.parsing.isolated import IsolatedParser
from techai_webutils.core.interfaces.parser import DocumentParser


def test_factory_selects_isolated_backend() -> None:
    """kind=isolated builds an ``IsolatedParser`` satisfying the ``DocumentParser`` seam.

    Why this test is important:
      - The default (and only) backend must be reachable through the factory and honour the tuning
        knobs, so a composition root selects it purely by config.
    What it tests:
      - ``parser_from_config`` returns an ``IsolatedParser`` that is a ``DocumentParser``, carrying the
        configured memory/timeout budgets.
    """
    config = ParserConfig(memory_bytes=123, timeout_seconds=7.0)
    parser = parser_from_config(config)
    assert isinstance(parser, IsolatedParser)
    assert isinstance(parser, DocumentParser)
    with patch("techai_webutils.clients.parsing.isolated.IsolatedParser") as parser_cls:
        parser_from_config(config)
    parser_cls.assert_called_once_with(memory_bytes=123, timeout_seconds=7.0)


def test_factory_rejects_unknown_kind() -> None:
    """An unknown parser kind fails loudly with ValueError (the Go NewFromConfig contract).

    What it tests:
      - ``parser_from_config`` with a bogus kind raises rather than returning a broken/None client.
    """
    with pytest.raises(ValueError, match="unknown parser kind"):
        parser_from_config(ParserConfig(kind="bogus"))  # type: ignore[arg-type]


def test_default_kind_is_isolated() -> None:
    """The default ``ParserConfig`` selects the isolated backend (behavior-preserving default)."""
    assert ParserConfig().kind is ParserKind.ISOLATED


def test_parsing_package_imports_without_heavy_deps() -> None:
    """Importing ``techai_webutils.clients.parsing`` + building the default parser pulls no heavy dep.

    Why this test is important:
      - makes markitdown/pymupdf an optional ``parsing`` extra; the parse seam must be
        importable and the default backend constructable with those packages absent — the property a
        non-parsing deployable relies on. Run in a fresh interpreter with the two packages hard-blocked
        so the check is honest even though this test venv has them installed.
    What it tests:
      - A subprocess that blocks ``markitdown``/``pymupdf`` imports can still import the package, call
        ``parser_from_config(ParserConfig())``, and finds neither package in ``sys.modules``.
    """
    script = (
        "import sys\n"
        "class _Blocker:\n"
        "    def find_spec(self, name, path=None, target=None):\n"
        "        if name in ('markitdown', 'pymupdf') or name.startswith(('markitdown.', 'pymupdf.')):\n"
        "            raise ImportError('blocked: ' + name)\n"
        "        return None\n"
        "sys.meta_path.insert(0, _Blocker())\n"
        "from techai_webutils.clients.parsing import ParserConfig, parser_from_config\n"
        "parser = parser_from_config(ParserConfig())\n"
        "assert type(parser).__name__ == 'IsolatedParser', type(parser)\n"
        "assert 'markitdown' not in sys.modules, 'markitdown was imported'\n"
        "assert 'pymupdf' not in sys.modules, 'pymupdf was imported'\n"
        "print('LAZY_OK')\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 0, result.stderr
    assert "LAZY_OK" in result.stdout
