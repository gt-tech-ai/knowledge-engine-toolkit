"""Tests for the isolated-parse start context: the forkserver preload and its warm-up."""

import multiprocessing as mp
from pathlib import Path

import pytest

from tests.child_python import run_python

_FORKSERVER = "forkserver" in mp.get_all_start_methods()


@pytest.mark.skipif(not _FORKSERVER, reason="the platform has no forkserver")
def test_warm_forkserver_imports_the_configured_preload(tmp_path: Path) -> None:
    """A configured module is imported by the forkserver when it is warmed.

    Why this test is important:
      - A forkserver child re-imports the parent's main module unless the server has
        already imported it; for a service that costs seconds per parse. The fix only
        works if the service's module reaches the server's preload, including a module
        found on a ``sys.path`` entry the service added, and if the warm-up starts the
        server before the first parse.
    What it tests:
      - In a fresh interpreter, ``configure_forkserver_preload([name])`` then
        ``warm_forkserver()`` makes the server import ``name`` (its import writes a
        marker file), while the parent process never imports it.

    Args:
        tmp_path: A scratch directory for the marker module and its marker file.

    """
    marker = tmp_path / "imported"
    module_dir = tmp_path / "mods"
    module_dir.mkdir()
    _ = (module_dir / "preload_marker_mod.py").write_text(
        f"from pathlib import Path\nPath({str(marker)!r}).write_text('x')\n"
    )
    script = (
        "import sys\n"
        f"sys.path.insert(0, {str(module_dir)!r})\n"
        "from techai_webutils.clients.parsing.isolated import (\n"
        "    configure_forkserver_preload, warm_forkserver)\n"
        "configure_forkserver_preload(['preload_marker_mod'])\n"
        "warm_forkserver()\n"
        "print('PARENT_IMPORTED', 'preload_marker_mod' in sys.modules)\n"
    )
    result = run_python(script)
    assert result.returncode == 0, result.stderr
    assert "PARENT_IMPORTED False" in result.stdout
    assert marker.exists()
