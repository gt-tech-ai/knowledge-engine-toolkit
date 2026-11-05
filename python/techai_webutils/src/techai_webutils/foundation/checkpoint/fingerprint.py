"""A stage's fingerprint: what must be unchanged for its stored output to be reused."""

from __future__ import annotations

import hashlib
import json
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.checkpoint import Stage


def fingerprint(stage: Stage, versions: Mapping[str, str], upstream: str | None) -> str:
    """Return the SHA-256 hex digest of the stage, its versions and its upstream's fingerprint.

    The input is canonical JSON (sorted keys, no spaces), so the order ``versions`` is built in
    never matters, while any version change, or any change upstream, gives a new fingerprint.
    ``upstream`` is the previous stage's fingerprint, or the content hash for the first stage.
    """
    canonical = json.dumps(
        {"stage": str(stage), "versions": dict(versions), "upstream": upstream},
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()
