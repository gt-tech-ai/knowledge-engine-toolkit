"""Tests for the process-local per-key fixed-window rate limiter (InMemoryRateLimiter)."""

import pytest

from techai_webutils.foundation.resilience.per_key_rate_limiter import InMemoryRateLimiter


class TestPerKeyRateLimiter:
    """Tests for the per-key rate limiter."""

    @pytest.mark.asyncio
    async def test_allows_up_to_limit_then_blocks(self) -> None:
        """Test that the limiter allows up to the per-key limit per window, then blocks.

        **Why this test is important:**
          - The window count is the throttle (e.g. per-recipient email caps); an off-by-one lets an
            extra send through or blocks a legitimate one.

        **What it tests:**
          - With a limit of 2, two allows succeed and the third is blocked — and a different key has
            its own independent allowance.
        """
        limiter = InMemoryRateLimiter(2)
        assert await limiter.allow("k") is True
        assert await limiter.allow("k") is True
        assert await limiter.allow("k") is False
        # A different key is tracked independently, not against k's exhausted window.
        assert await limiter.allow("other") is True

    @pytest.mark.asyncio
    async def test_window_resets_after_elapse(self) -> None:
        """Test that a key's allowance resets once its window elapses.

        **Why this test is important:**
          - Without a window reset the limiter would permanently block a key after the first window;
            it must recover each window.

        **What it tests:**
          - With an injected clock, a blocked key is allowed again after the window passes.
        """
        now = {"t": 0.0}
        limiter = InMemoryRateLimiter(1, window_seconds=10.0, clock=lambda: now["t"])
        assert await limiter.allow("k") is True
        assert await limiter.allow("k") is False
        now["t"] = 11.0
        assert await limiter.allow("k") is True


class TestPerKeyRateLimiterEviction:
    """Covers the expired-key eviction path that bounds the limiter's memory."""

    @pytest.mark.asyncio
    async def test_eviction_preserves_rate_limiting(self) -> None:
        """Reaching the eviction threshold drops expired keys without corrupting limits.

        **Why this test is important:**
          - Eviction bounds memory under many distinct keys; it must free only
            fully-elapsed windows and never let a still-active key's count leak or
            an expired key's stale window block a fresh request.

        **What it tests:**
          - After the threshold is crossed and expired keys are evicted, an in-window
            second request is still blocked and an evicted key gets a fresh allowance.
        """
        now = {"t": 0.0}
        limiter = InMemoryRateLimiter(
            1,
            window_seconds=10.0,
            clock=lambda: now["t"],
            evict_threshold=2,
        )

        assert await limiter.allow("a") is True
        assert await limiter.allow("b") is True  # 2 keys tracked — at the threshold
        now["t"] = 20.0  # a and b windows have fully elapsed
        assert await limiter.allow("c") is True  # crossing threshold triggers eviction

        # Rate limiting is intact after eviction:
        assert await limiter.allow("c") is False  # second in-window request blocked
        assert await limiter.allow("a") is True  # evicted key gets a fresh window
