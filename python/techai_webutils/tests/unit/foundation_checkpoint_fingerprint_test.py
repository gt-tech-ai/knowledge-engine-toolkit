"""Property tests for the stage fingerprint (``foundation.checkpoint.fingerprint``)."""

from __future__ import annotations

from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.core.interfaces.checkpoint import Stage
from techai_webutils.foundation.checkpoint import fingerprint

_EXPECTED_DIGEST_COUNT = 64

_versions = st.dictionaries(
    st.text(min_size=1, max_size=8), st.text(max_size=8), max_size=6
)
_upstream = st.one_of(st.none(), st.text(max_size=16))


@given(versions=_versions, upstream=_upstream, stage=st.sampled_from(list(Stage)))
def test_order_of_versions_never_matters(
    versions: dict[str, str], upstream: str | None, stage: Stage
) -> None:
    """The fingerprint ignores the order the versions mapping was built in.

    **Why this test is important:**
      - Versions come from config and code in no fixed order; an order-sensitive hash
        would miss every checkpoint after a harmless reordering and re-run the whole
        pipeline.

    **What it tests:**
      - The same versions inserted in reverse order give the same 64-character hex digest.
    """
    reordered = dict(reversed(list(versions.items())))
    digest = fingerprint(stage, versions, upstream)
    assert digest == fingerprint(stage, reordered, upstream)
    assert len(digest) == _EXPECTED_DIGEST_COUNT


@given(
    versions=_versions,
    upstream=_upstream,
    stage=st.sampled_from(list(Stage)),
    name=st.text(min_size=1, max_size=8),
    value=st.text(max_size=8),
)
def test_any_version_or_upstream_change_changes_it(
    versions: dict[str, str], upstream: str | None, stage: Stage, name: str, value: str
) -> None:
    """A changed version, a changed upstream or another stage gives another fingerprint.

    **Why this test is important:**
      - A fingerprint that missed a version bump would reuse stale output, so a fixed
        parser or a new embedding model would never reach already-ingested documents.

    **What it tests:**
      - Setting one version to a new value, changing the upstream, or changing the stage
        each change the digest.
    """
    digest = fingerprint(stage, versions, upstream)
    if versions.get(name) != value:
        assert fingerprint(stage, {**versions, name: value}, upstream) != digest
    assert fingerprint(stage, versions, f"{upstream}-changed") != digest
    other = next(s for s in Stage if s is not stage)
    assert fingerprint(other, versions, upstream) != digest
