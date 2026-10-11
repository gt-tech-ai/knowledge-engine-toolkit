"""Tests for BaseRepository delegating to a Store (mocked via the Store interface)."""

from unittest.mock import MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import NotFoundError
from techai_webutils.core.interfaces.store import Store
from techai_webutils.repos.repository import BaseRepository


class TestBaseRepository:
    """Test suite for BaseRepository CRUD delegation to the underlying store."""

    @pytest.mark.asyncio
    async def test_get_delegates_to_store(self) -> None:
        """Test that get retrieves an entity by ID from the backing store.

        **Why this test is important:**
          - BaseRepository is the data access foundation for all domain services
          - Incorrect delegation would silently return wrong data across every service
          - Get-by-ID is the most frequently called repository operation

        **What it tests:**
          - The store's entity is returned, and get is delegated with the requested ID
        """
        store = MagicMock(spec=Store)
        store.get.return_value = "entity-1"
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        result = await repo.get("1")
        assert result == "entity-1"
        store.get.assert_awaited_once_with("1")

    @pytest.mark.asyncio
    async def test_get_not_found(self) -> None:
        """Test that getting a nonexistent entity raises NotFoundError.

        **Why this test is important:**
          - Missing entity lookups must produce a consistent error type across all repositories
          - NotFoundError is mapped to HTTP 404 in the controller layer
          - Returning None instead of raising would cause silent failures in service logic

        **What it tests:**
          - A store that raises NotFoundError propagates through the repository
        """
        store = MagicMock(spec=Store)
        store.get.side_effect = NotFoundError("not found: missing")
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        with pytest.raises(NotFoundError):
            await repo.get("missing")

    @pytest.mark.asyncio
    async def test_create_delegates_to_store(self) -> None:
        """Test that create persists a new entity through the store.

        **Why this test is important:**
          - Entity creation is the entry point for all new data in the system
          - The repository must faithfully pass through the entity without modification
          - Incorrect delegation could silently drop or alter persisted data

        **What it tests:**
          - The store's created entity is returned, and create is delegated with the entity
        """
        store = MagicMock(spec=Store)
        store.create.return_value = "new-entity"
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        result = await repo.create("new-entity")
        assert result == "new-entity"
        store.create.assert_awaited_once_with("new-entity")

    @pytest.mark.asyncio
    async def test_list_delegates_to_store(self) -> None:
        """Test that list returns a paginated result from the store.

        **Why this test is important:**
          - List operations power all browse/search UIs and must return accurate page metadata
          - Incorrect total counts break pagination controls in the frontend
          - The repository must correctly forward filter and page parameters to the store

        **What it tests:**
          - Total count and item count from the store's Page pass through unchanged
        """
        store = MagicMock(spec=Store)
        store.list.return_value = Page(
            items=["a", "b"], total=2, page_size=10, page_number=1
        )
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        page = await repo.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 2
        assert len(page.items) == 2
        store.list.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_update_delegates_to_store(self) -> None:
        """Test that update replaces an existing entity in the store.

        **Why this test is important:**
          - Update is the primary mutation path for existing entities
          - The repository must pass the new value through without alteration
          - Incorrect delegation could cause silent data corruption

        **What it tests:**
          - The store's updated value is returned, and update is delegated with (id, entity)
        """
        store = MagicMock(spec=Store)
        store.update.return_value = "new"
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        result = await repo.update("1", "new")
        assert result == "new"
        store.update.assert_awaited_once_with("1", "new")

    @pytest.mark.asyncio
    async def test_delete_delegates_to_store(self) -> None:
        """Test that delete removes an entity so it no longer exists.

        **Why this test is important:**
          - Delete must actually remove the entity, not just mark it
          - Failed deletion would leave orphaned data visible to queries
          - The exists check confirms the store state was actually modified

        **What it tests:**
          - delete is delegated with the requested ID, and a subsequent exists returns False
        """
        store = MagicMock(spec=Store)
        store.exists.return_value = False
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        await repo.delete("1")
        assert await repo.exists("1") is False
        store.delete.assert_awaited_once_with("1")
        store.exists.assert_awaited_once_with("1")

    @pytest.mark.asyncio
    async def test_exists_delegates_to_store(self) -> None:
        """Test that exists correctly reports presence and absence of entities.

        **Why this test is important:**
          - Exists checks are used for idempotency guards and duplicate detection
          - False positives would skip necessary creates; false negatives would cause duplicates
          - Both the True and False paths must be verified for correctness

        **What it tests:**
          - The store's boolean result passes through for a present and an absent ID
        """
        store = MagicMock(spec=Store)
        store.exists.side_effect = lambda entity_id: entity_id == "1"
        repo: BaseRepository[str, dict[str, object], str] = BaseRepository(store)

        assert await repo.exists("1") is True
        assert await repo.exists("2") is False
