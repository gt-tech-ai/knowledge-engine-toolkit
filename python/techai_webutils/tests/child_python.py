"""Run a Python snippet in a child interpreter, for tests of import-time behaviour."""

import asyncio
import sys
from typing import NamedTuple


class ChildResult(NamedTuple):
    """What a finished child interpreter reported."""

    returncode: int | None
    """The exit status; ``None`` if the child never exited."""

    stdout: str
    """What the child wrote to stdout."""

    stderr: str
    """What the child wrote to stderr."""


async def _run(script: str) -> ChildResult:
    """Run ``script`` under the current interpreter and wait for it.

    Args:
        script: The Python source to run.

    Returns:
        The child's exit code and decoded output.

    """
    process = await asyncio.create_subprocess_exec(
        sys.executable,
        "-c",
        script,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    stdout, stderr = await process.communicate()
    return ChildResult(process.returncode, stdout.decode(), stderr.decode())


def run_python(script: str) -> ChildResult:
    """Run ``script`` in a fresh interpreter, so its imports start from nothing.

    Args:
        script: The Python source to run.

    Returns:
        The child's exit code and decoded output.

    """
    return asyncio.run(_run(script))
