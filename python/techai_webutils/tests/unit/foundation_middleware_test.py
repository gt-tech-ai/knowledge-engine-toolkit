"""Tests for middleware chain and request ID middleware."""

from __future__ import annotations

import asyncio

import pytest

from techai_webutils.foundation.middleware.chain import chain
from techai_webutils.foundation.middleware.request_id import (
    get_request_id,
    request_id_middleware,
    set_request_id,
)


class TestChain:
    """Test suite for middleware composition."""

    def test_empty_chain(self) -> None:
        """Test that composing zero middleware returns the handler untouched.

        **Why this test is important:**
          - Services with no middleware configured must still get a working handler
          - An identity composition is the base case the recursive folding relies on;
            if it mangled the handler, every chain built on top would be wrong

        **What it tests:**
          - chain() with no arguments returns the input handler unchanged
        """
        composed = chain()
        assert composed("handler") == "handler"

    def test_single_middleware(self) -> None:
        """Test that a single middleware is applied to the handler.

        **Why this test is important:**
          - The one-middleware case proves the wrapper is actually invoked, not just
            stored; a chain that silently dropped its only middleware would disable
            cross-cutting concerns (auth, logging) without any error
          - It isolates "does the middleware run" from the ordering question

        **What it tests:**
          - chain(upper) produces a handler that applies upper to its input
        """

        def upper(h: str) -> str:
            return h.upper()

        composed = chain(upper)
        assert composed("hello") == "HELLO"

    def test_multiple_middleware_order(self) -> None:
        """Test that chained middleware nest outermost-first: chain(a, b, c)(h) == a(b(c(h))).

        **Why this test is important:**
          - Middleware order is load-bearing behavior, not cosmetic: an auth check
            must wrap (run before) the handler it guards, and a recovery wrapper must
            sit outermost to catch everything inside. Reversed nesting would let a
            panic escape recovery or run business logic before authorization
          - This is the contract that mirrors the Go chain.go, so both stacks must agree

        **What it tests:**
          - chain(a, b, c) applies a outermost and c innermost, yielding "a-b-c-h"
        """

        def prefix_a(h: str) -> str:
            return "a-" + h

        def prefix_b(h: str) -> str:
            return "b-" + h

        def prefix_c(h: str) -> str:
            return "c-" + h

        composed = chain(prefix_a, prefix_b, prefix_c)
        assert composed("h") == "a-b-c-h"


class TestRequestIdMiddleware:
    """Test suite for request ID context propagation."""

    @pytest.mark.asyncio
    async def test_generates_uuid_when_none_provided(self) -> None:
        """Test that the middleware mints a UUID request ID when none is supplied.

        **Why this test is important:**
          - Every request must be traceable; if an inbound request carries no
            correlation ID, the middleware has to generate one so logs and traces for
            that request can still be stitched together. Skipping generation would
            leave un-correlatable requests in production telemetry

        **What it tests:**
          - Inside the wrapped handler, get_request_id() returns a non-empty,
            36-character UUID-formatted value when no request_id kwarg is passed
        """
        captured_id: str = ""

        async def handler() -> None:
            await asyncio.sleep(0)
            nonlocal captured_id
            captured_id = get_request_id()

        wrapped = request_id_middleware(handler)
        await wrapped()
        assert captured_id
        assert len(captured_id) == 36  # UUID format

    @pytest.mark.asyncio
    async def test_uses_provided_request_id(self) -> None:
        """Test that a caller-supplied request ID is propagated, not overwritten.

        **Why this test is important:**
          - Distributed tracing depends on honoring an upstream correlation ID so a
            single logical request keeps one ID across service hops. If the middleware
            generated a fresh UUID even when given one, traces would fragment at every
            service boundary and break end-to-end debugging

        **What it tests:**
          - Passing request_id="custom-123" makes get_request_id() return exactly
            "custom-123" inside the handler
        """
        captured_id: str = ""

        async def handler() -> None:
            await asyncio.sleep(0)
            nonlocal captured_id
            captured_id = get_request_id()

        wrapped = request_id_middleware(handler)
        await wrapped(request_id="custom-123")
        assert captured_id == "custom-123"

    def test_set_and_get(self) -> None:
        """Test that set_request_id stores a value that get_request_id reads back.

        **Why this test is important:**
          - The request ID is carried in a contextvar, and the set/get pair is the
            primitive every framework adapter relies on to stash and later read the
            correlation ID. If the two disagreed, IDs set at the edge would be
            invisible to downstream logging
          - The test also exercises resetting the contextvar via the returned token,
            which is how the middleware avoids leaking IDs between requests

        **What it tests:**
          - After set_request_id("test-id"), get_request_id() returns "test-id"
        """
        token = set_request_id("test-id")
        try:
            assert get_request_id() == "test-id"
        finally:
            token.var.reset(token)
