"""The ``Client families are independent`` import contract.

It lists every ``clients/`` package.
"""

from __future__ import annotations

import tomllib
from pathlib import Path
from typing import cast

import techai_webutils.clients as clients_pkg

_PYPROJECT = Path(__file__).resolve().parents[2] / "pyproject.toml"
_CONTRACT = "Client families are independent"


def _contract_modules() -> set[str]:
    """Return the modules the independence contract in ``pyproject.toml`` names.

    Returns:
        The modules the independence contract in ``pyproject.toml`` names.

    """
    data = tomllib.loads(_PYPROJECT.read_text(encoding="utf-8"))
    contracts = cast("list[dict[str, object]]", data["tool"]["importlinter"]["contracts"])
    contract = next(c for c in contracts if c["name"] == _CONTRACT)
    return set(cast("list[str]", contract["modules"]))


def _client_packages() -> set[str]:
    """Return every subpackage of ``techai_webutils.clients`` on disk.

    Returns:
        Every subpackage of ``techai_webutils.clients`` on disk.

    """
    root = Path(next(iter(clients_pkg.__path__)))
    return {
        f"techai_webutils.clients.{p.name}"
        for p in root.iterdir()
        if (p / "__init__.py").is_file()
    }


def test_independence_contract_lists_every_client_family() -> None:
    """Test that the independence contract lists every ``clients/`` package.

    And no stale one.

    **Why this test is important:**
      - import-linter checks only the modules a contract names, so a new client family
        left off the list could import a sibling family unnoticed, coupling backends the
        layer model keeps apart.

    **What it tests:**
      - the contract's modules equal the subpackages of ``techai_webutils.clients`` on
        disk
    """
    assert _contract_modules() == _client_packages()
