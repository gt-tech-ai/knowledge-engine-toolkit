"""Tests for REST adapter pattern."""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock

import pytest

from techai_webutils.controllers.adapt import adapt, adapt_no_content
from techai_webutils.controllers.base import BaseController
from techai_webutils.core.errors.errors import NotFoundError


class TestAdapt:
    """Test suite for the adapt bridge function."""

    @pytest.mark.asyncio
    async def test_success(self) -> None:
        """Test that adapt parses the request, runs the handler, and returns its result with the status.

        **Why this test is important:**
          - adapt is the bridge every typed REST handler is mounted through; the happy path must wire correctly
          - It must thread the parsed request into the handler and surface the handler's response unchanged
          - A break here would mis-serialize or drop responses across all REST endpoints

        **What it tests:**
          - The success status (200) is returned
          - The handler's response (derived from the parsed id) is carried through as the data payload
        """
        ctrl = BaseController()

        async def handler(req: MagicMock) -> MagicMock:
            await asyncio.sleep(0)
            # Derive the response from the parsed request so the id is threaded end to end.
            resp = MagicMock()
            resp.name = "entity-" + req.id
            return resp

        async def parse(raw: dict[str, str]) -> MagicMock:
            await asyncio.sleep(0)
            req = MagicMock()
            req.id = raw["id"]
            return req

        adapted = adapt(ctrl, handler, parse, status=200)
        result = await adapted({"id": "1"})
        assert result["status"] == 200
        assert result["data"].name == "entity-1"

    @pytest.mark.asyncio
    async def test_error(self) -> None:
        """Test that a handler exception is converted to its mapped HTTP error status.

        **Why this test is important:**
          - Handlers signal failures by raising domain errors; adapt must turn those into HTTP responses
          - If exceptions escaped instead of being mapped, the framework would 500 on every domain error
          - Confirms the error path routes through the controller's error mapping (NotFound → 404)

        **What it tests:**
          - A handler raising NotFoundError yields a response with status 404
        """
        ctrl = BaseController()

        async def handler(_req: MagicMock) -> MagicMock:
            await asyncio.sleep(0)
            msg = "not found"
            raise NotFoundError(msg)

        async def parse(raw: dict[str, str]) -> MagicMock:
            await asyncio.sleep(0)
            req = MagicMock()
            req.id = raw["id"]
            return req

        adapted = adapt(ctrl, handler, parse)
        result = await adapted({"id": "1"})
        assert result["status"] == 404


class TestAdaptNoContent:
    """Test suite for the adapt_no_content bridge function."""

    @pytest.mark.asyncio
    async def test_success(self) -> None:
        """Test that adapt_no_content returns 204 after a handler that produces no body.

        **Why this test is important:**
          - Mutating endpoints (e.g. DELETE) succeed with no payload and must report 204, not 200
          - The adapter must ignore the handler's return value and emit the correct no-content status
          - A wrong status here would mislead clients about whether a body is present

        **What it tests:**
          - A body-less handler yields a response with status 204
        """
        ctrl = BaseController()

        async def handler(req: MagicMock) -> None:
            pass

        async def parse(raw: dict[str, str]) -> MagicMock:
            await asyncio.sleep(0)
            req = MagicMock()
            req.id = raw["id"]
            return req

        adapted = adapt_no_content(ctrl, handler, parse)
        result = await adapted({"id": "1"})
        assert result["status"] == 204
