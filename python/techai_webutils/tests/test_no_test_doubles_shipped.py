"""Packaging invariant: shared test doubles never ship in the wheel."""

from pathlib import Path


def test_no_fake_doubles_in_shipped_src() -> None:
    """Test doubles are test-only and never appear in the packaged src/ tree.

    Why this test is important:
      - Test-only code must not ship in the production wheel; a fake_*.py
        creeping back into src/techai_webutils would ship to every downstream consumer of the
        techai-webutils wheel. This pins the "doubles are test-only" invariant cheaply — no wheel
        build needed on every run (the full build+grep assertion is a manual/CI verification).
        Doubles are now built inline with unittest.mock (MagicMock/AsyncMock, spec-bound to the
        real core interface) in each test; there is no shared fakes package.

    What it tests:
      - No fake_*.py exists anywhere under src/techai_webutils, and the old mocks/ package is gone.
    """
    src = Path(__file__).resolve().parents[1] / "src" / "techai_webutils"
    stray = list(src.rglob("fake_*.py"))
    assert not stray, f"test doubles must not ship in the wheel: {stray}"
    assert not (src / "mocks").exists(), "the mocks/ package must not ship in src/"
