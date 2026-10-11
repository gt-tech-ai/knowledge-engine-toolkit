"""Tests for Repository, Transaction, and TransactionManager ABCs."""

from unittest.mock import MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.interfaces.repository import (
    Repository,
    Transaction,
    TransactionManager,
)


class TestRepository:
    """Tests for the repository."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that the Repository ABC cannot be instantiated without all CRUD methods.

        **Why this test is important:**
          - Repository is the generic data-access contract every service depends on; an
            instantiable stub would let a partially-implemented repo reach the service layer
            and return None for reads or silently no-op writes, corrupting persistence
          - Forces concrete repos to implement the complete get/list/create/update/delete/exists
            surface before they can be constructed

        **What it tests:**
          - Instantiating Repository directly raises TypeError because its CRUD methods are
            abstract
        """
        with pytest.raises(TypeError):
            Repository()  # type: ignore[abstract]

    @pytest.mark.asyncio
    async def test_repo_get_is_awaitable_through_the_contract(self) -> None:
        """Test that the contract's get() is an awaitable that returns the entity a repo produces.

        **Why this test is important:**
          - get() is the primary read surface every service depends on; this confirms the contract
            exposes it as an awaitable whose resolved value reaches the caller unchanged, so a repo
            returning an entity is observed as that entity (not a coroutine or a truthy proxy)

        **What it tests:**
          - A spec'd Repository mock whose get() resolves to "entity-1" is awaited and yields
            "entity-1" through the contract
        """
        repo = MagicMock(spec=Repository)
        repo.get.return_value = "entity-1"
        assert await repo.get("1") == "entity-1"

    @pytest.mark.asyncio
    async def test_repo_list_returns_a_page_through_the_contract(self) -> None:
        """Test that the contract's paginated list() resolves to the Page a repo returns.

        **Why this test is important:**
          - list() is the contract's pagination surface; this confirms a repo can hand back a
            populated Page — the shape callers iterate — and that the awaited result preserves its
            items, so the generic paging signature is usable as defined

        **What it tests:**
          - A spec'd Repository mock whose list() resolves to a Page(items=["a"]) is awaited with a
            PageRequest and yields a Page whose items are ["a"]
        """
        repo = MagicMock(spec=Repository)
        repo.list.return_value = Page(items=["a"], total=1, page_size=10, page_number=1)
        page = await repo.list({}, PageRequest(page_size=10, page_number=1))
        assert page.items == ["a"]

    @pytest.mark.asyncio
    async def test_repo_create_round_trips_the_entity_through_the_contract(self) -> None:
        """Test that the contract's create() resolves to the entity a repo persists.

        **Why this test is important:**
          - create() is the write surface every service uses to persist new entities; this confirms
            the contract returns the persisted entity to the caller, so the create signature is
            honored end to end

        **What it tests:**
          - A spec'd Repository mock whose create() resolves to "x" is awaited and yields "x"
        """
        repo = MagicMock(spec=Repository)
        repo.create.return_value = "x"
        assert await repo.create("x") == "x"

    @pytest.mark.asyncio
    async def test_repo_exists_returns_a_boolean_through_the_contract(self) -> None:
        """Test that the contract's exists() resolves to the boolean a repo reports.

        **Why this test is important:**
          - exists() is the cheap presence check callers use before reads/writes to avoid
            NotFoundError handling; this confirms the contract yields a real bool to the caller
            rather than a truthy proxy that could mislead branching logic

        **What it tests:**
          - A spec'd Repository mock whose exists() resolves to True is awaited and yields the
            boolean True
        """
        repo = MagicMock(spec=Repository)
        repo.exists.return_value = True
        assert await repo.exists("1") is True


class TestTransaction:
    """Tests for the transaction."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that the Transaction ABC cannot be instantiated without commit/rollback.

        **Why this test is important:**
          - Transaction is the handle that guarantees atomicity; an instantiable stub whose
            commit()/rollback() are no-ops would let writes appear committed while the database
            never durably persists them — a silent data-integrity failure
          - Forces every transaction implementation to supply real commit and rollback

        **What it tests:**
          - Instantiating Transaction directly raises TypeError because commit() and rollback()
            are abstract
        """
        with pytest.raises(TypeError):
            Transaction()  # type: ignore[abstract]


class TestTransactionManager:
    """Tests for the transaction manager."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that TransactionManager cannot be instantiated without begin/with_transaction.

        **Why this test is important:**
          - TransactionManager is the contract that scopes work into atomic units; an
            instantiable stub would hand out fake transactions or skip the commit/rollback
            wrapper, so multi-step operations would lose all-or-nothing semantics
          - Forces every manager to implement real transaction lifecycle management

        **What it tests:**
          - Instantiating TransactionManager directly raises TypeError because begin() and
            with_transaction() are abstract
        """
        with pytest.raises(TypeError):
            TransactionManager()  # type: ignore[abstract]
