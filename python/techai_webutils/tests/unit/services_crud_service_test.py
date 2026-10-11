"""Tests for BaseCrudService delegating to a Repository (mocked via the Repository interface)."""

from unittest.mock import MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import NotFoundError
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.services.crud_service import BaseCrudService


class TestBaseCrudService:
    """Test suite for BaseCrudService CRUD delegation to the underlying repository."""

    @pytest.mark.asyncio
    async def test_get_delegates_to_repo(self) -> None:
        """Test that get retrieves an entity by ID from the repository.

        **Why this test is important:**
          - BaseCrudService is the business logic foundation for all domain services
          - Incorrect delegation would silently return wrong data across every service
          - Get-by-ID is the most frequently called service operation

        **What it tests:**
          - The repository's entity is returned, and get is delegated with the requested ID
        """
        repo = MagicMock(spec=Repository)
        repo.get.return_value = "entity-1"
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        assert await svc.get("1") == "entity-1"
        repo.get.assert_awaited_once_with("1")

    @pytest.mark.asyncio
    async def test_get_not_found(self) -> None:
        """Test that getting a nonexistent entity raises NotFoundError.

        **Why this test is important:**
          - Missing entity lookups must propagate NotFoundError from the repository layer
          - The service layer must not swallow or transform not-found errors
          - NotFoundError is mapped to HTTP 404 in the controller layer

        **What it tests:**
          - A repository that raises NotFoundError propagates through the service
        """
        repo = MagicMock(spec=Repository)
        repo.get.side_effect = NotFoundError("not found: missing")
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        with pytest.raises(NotFoundError):
            await svc.get("missing")

    @pytest.mark.asyncio
    async def test_create_delegates_to_repo(self) -> None:
        """Test that create persists a new entity through the repository.

        **Why this test is important:**
          - Entity creation is the entry point for all new data in the system
          - The service must faithfully delegate to the repository without modification
          - Incorrect delegation could silently drop or alter persisted data

        **What it tests:**
          - The repository's created entity is returned, and create is delegated with the entity
        """
        repo = MagicMock(spec=Repository)
        repo.create.return_value = "new"
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        result = await svc.create("new")
        assert result == "new"
        repo.create.assert_awaited_once_with("new")

    @pytest.mark.asyncio
    async def test_list_delegates_to_repo(self) -> None:
        """Test that list returns a paginated result from the repository.

        **Why this test is important:**
          - List operations power all browse/search UIs and must return accurate totals
          - The service must correctly forward filter and page parameters to the repository
          - Incorrect total counts break pagination controls in the frontend

        **What it tests:**
          - The repository's Page total passes through unchanged, and list is delegated
        """
        repo = MagicMock(spec=Repository)
        repo.list.return_value = Page(
            items=["a", "b"], total=2, page_size=10, page_number=1
        )
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        page = await svc.list({}, PageRequest(page_size=10, page_number=1))
        assert page.total == 2
        repo.list.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_update_delegates_to_repo(self) -> None:
        """Test that update replaces an existing entity in the repository.

        **Why this test is important:**
          - Update is the primary mutation path for existing entities
          - The service must pass the new value through without alteration
          - Incorrect delegation could cause silent data corruption

        **What it tests:**
          - The repository's updated value is returned, and update is delegated with (id, entity)
        """
        repo = MagicMock(spec=Repository)
        repo.update.return_value = "new"
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        result = await svc.update("1", "new")
        assert result == "new"
        repo.update.assert_awaited_once_with("1", "new")

    @pytest.mark.asyncio
    async def test_delete_delegates_to_repo(self) -> None:
        """Test that delete removes an entity through the repository.

        **Why this test is important:**
          - Delete must actually reach the repository, not be silently ignored
          - The service must not swallow the deletion
          - Delegation with the correct ID is what removes the entity end-to-end

        **What it tests:**
          - delete is delegated to the repository with the requested ID
        """
        repo = MagicMock(spec=Repository)
        svc: BaseCrudService[str, dict[str, object], str] = BaseCrudService(repo)

        await svc.delete("1")
        repo.delete.assert_awaited_once_with("1")
