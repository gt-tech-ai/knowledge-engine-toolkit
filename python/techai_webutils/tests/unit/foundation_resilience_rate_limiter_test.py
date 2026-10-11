"""Tests for TokenBucketRateLimiter."""

import pytest

from techai_webutils.foundation.resilience.rate_limiter import TokenBucketRateLimiter


class TestTokenBucketRateLimiter:
    """Test suite for TokenBucketRateLimiter token bucket rate limiting."""

    def test_allow_within_burst(self) -> None:
        """Test that requests within the burst capacity are allowed immediately.

        **Why this test is important:**
          - Burst capacity handles legitimate traffic spikes without rejecting requests
          - Rate limiters protect downstream services from overload
          - Requests within burst must be served without delay for user experience

        **What it tests:**
          - allow() returns True for all requests up to the burst limit of 3
        """
        limiter = TokenBucketRateLimiter(rate=10.0, burst=3)
        assert limiter.allow() is True
        assert limiter.allow() is True
        assert limiter.allow() is True

    def test_deny_when_exhausted(self) -> None:
        """Test that requests are denied once the burst tokens are exhausted.

        **Why this test is important:**
          - Denying excess requests prevents resource exhaustion on downstream services
          - Without denial, rate limiting would be ineffective against traffic spikes
          - Clients receiving denials can implement backoff or queue their requests

        **What it tests:**
          - First allow() returns True (consumes the single burst token)
          - Second allow() returns False (no tokens remaining)
        """
        limiter = TokenBucketRateLimiter(rate=10.0, burst=1)
        assert limiter.allow() is True
        assert limiter.allow() is False

    @pytest.mark.asyncio
    async def test_wait_until_token_available(self) -> None:
        """Test that wait blocks until a token becomes available through refill.

        **Why this test is important:**
          - wait() provides a blocking alternative to allow() for queue-based consumers
          - Callers that can tolerate delay use wait() instead of failing immediately
          - The token refill mechanism must actually replenish tokens over time

        **What it tests:**
          - After exhausting burst, wait() eventually succeeds at high refill rate (1000/sec)
        """
        limiter = TokenBucketRateLimiter(rate=1000.0, burst=1)
        assert limiter.allow() is True  # exhaust burst
        # wait should refill within a short time at 1000/sec
        await limiter.wait()
        # If we get here, wait succeeded

    def test_deny_then_refill(self) -> None:
        """Test that tokens refill over time, re-enabling previously denied requests.

        **Why this test is important:**
          - Token refill ensures the rate limiter recovers and does not permanently block traffic
          - The refill rate must match the configured rate for predictable throughput
          - Without refill, a single burst would permanently exhaust the limiter

        **What it tests:**
          - Two allow() calls succeed, third is denied (burst=2 exhausted)
          - After sleeping 20ms at 100 tokens/sec, allow() succeeds again
        """
        # Use a low rate so exhaustion is stable, then sleep to refill
        limiter = TokenBucketRateLimiter(rate=100.0, burst=2)
        assert limiter.allow() is True
        assert limiter.allow() is True
        assert limiter.allow() is False
        # Sleep enough for at least 1 token at 100/sec
        import time

        time.sleep(0.02)
        assert limiter.allow() is True
